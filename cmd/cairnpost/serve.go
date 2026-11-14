package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/config"
	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/driver/cloudflare"
	"github.com/mettjs/cairnpost/internal/driver/smtp"
	"github.com/mettjs/cairnpost/internal/events"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/metrics"
	"github.com/mettjs/cairnpost/internal/prune"
	"github.com/mettjs/cairnpost/internal/session"
	"github.com/mettjs/cairnpost/internal/stats"
	"github.com/mettjs/cairnpost/internal/store/postgres"
	"github.com/mettjs/cairnpost/internal/suppress"
	"github.com/mettjs/cairnpost/internal/webhook"
	"github.com/mettjs/cairnpost/migrations"
)

// open connects to the database the environment names.
func open(ctx context.Context) (*postgres.DB, error) {
	dsn := os.Getenv("CAIRNPOST_POSTGRES_DSN")
	if dsn == "" {
		return nil, errors.New("set CAIRNPOST_POSTGRES_DSN to the database's address")
	}
	return postgres.Open(ctx, dsn)
}

// migrate applies the migrations this binary carries.
func migrate(ctx context.Context) error {
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	applied, err := db.Migrate(ctx, migrations.FS)
	for _, name := range applied {
		fmt.Println("applied", name)
	}
	if err == nil && len(applied) == 0 {
		fmt.Println("the schema is up to date")
	}
	return err
}

// serve runs the HTTP API until the context ends, then lets requests in
// flight finish.
func serve(ctx context.Context) error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	db, err := postgres.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	applied, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		return err
	}
	for _, name := range applied {
		log.Info("applied a migration", "name", name)
	}

	// The driver is chosen here and nowhere else.
	var (
		drv      driver.Driver
		captured func() []api.CapturedEmail
		source   driver.Source
		// neverTold is a driver whose upstream reports nothing of an
		// email after it took it.
		neverTold bool
	)
	switch cfg.Driver {
	case capture.Name:
		kept := capture.New(cfg.CaptureKeep, time.Now)
		drv, captured = kept, func() []api.CapturedEmail { return capturedEmails(kept) }
		log.Warn("the capture driver is in use: emails are kept in memory and sent to nobody")
	case cloudflare.Name:
		cf := cloudflare.New(cfg.Cloudflare.AccountID, cfg.Cloudflare.APIToken)
		drv = cf
		if cfg.Cloudflare.QueueID != "" {
			events := cf.Events(cfg.Cloudflare.QueueID)
			if cfg.ContentRetention != 0 {
				// Kept under the same rule as what emails say.
				events = events.WithDetails()
			}
			source = events
		}
	case smtp.Name:
		drv = smtp.New(smtp.Options{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Security: cfg.SMTP.Security,
			Username: cfg.SMTP.Username, Password: cfg.SMTP.Password, Hello: cfg.SMTP.Hello}, time.Now)
		neverTold = true
		if !strings.Contains(cfg.SMTP.Hello, ".") {
			log.Warn("the name the mail server is greeted with is not a full host name; some servers refuse such a one (CAIRNPOST_SMTP_HELLO)")
		}
		log.Warn("the smtp driver is in use: a mail server tells nothing of an email after it took it, " +
			"so records stop at \"sent\" and no address is suppressed by itself")
	default:
		return fmt.Errorf("no driver is built for %q", cfg.Driver)
	}

	// The reader of delivery events runs beside the API and stops with it.
	var reader *events.Reader
	reading := make(chan struct{})
	switch {
	case source != nil:
		reader = events.New(db, db.Ping, source, drv.Name(), events.Options{Poll: cfg.EventsPoll,
			Batch: cfg.EventsBatch, Lease: cfg.EventsLease, Retry: cfg.EventsRetry, Grace: cfg.EventsGrace,
			KeepDetails: cfg.ContentRetention}, time.Now, log)
		go func() { defer close(reading); reader.Run(ctx) }()
	case captured == nil:
		close(reading)
		log.Warn("delivery events are not read: an email's record stops at \"sent\", and a bounce is never known",
			"driver", drv.Name())
	default:
		close(reading)
	}

	// Webhooks are sent beside the API too, and stop with it.
	poster := webhook.NewPoster(cfg.Webhook.Timeout, cfg.Webhook.AllowPrivate, cfg.Webhook.PerEndpoint, version, time.Now)
	hooks := webhook.NewWorker(db, poster, webhook.Options{Poll: cfg.Webhook.Poll, Workers: cfg.Webhook.Workers,
		PerEndpoint: cfg.Webhook.PerEndpoint, Retries: cfg.Webhook.Retries,
		PauseAfter: cfg.Webhook.PauseAfter}, log)
	sending := make(chan struct{})
	go func() { defer close(sending); hooks.Run(ctx) }()
	if cfg.Webhook.AllowPrivate {
		log.Warn("webhook endpoints may be plain http and at private addresses (CAIRNPOST_WEBHOOK_ALLOW_PRIVATE)")
	}

	// What has been kept long enough is deleted beside the API as well.
	pruner := prune.New(db, prune.Options{Every: cfg.PruneEvery, KeyTTL: cfg.IdempotencyTTL, Keep: cfg.Retention,
		KeepCounts: cfg.CountsRetention, Unanswered: cfg.AbandonedAfter()}, log)
	pruning := make(chan struct{})
	go func() { defer close(pruning); pruner.Run(ctx) }()
	if cfg.Retention == 0 {
		log.Warn("emails' records are kept for ever (CAIRNPOST_RETENTION=off): the database only grows")
	}

	if cfg.ContentRetention != 0 {
		log.Warn("what emails say is kept (CAIRNPOST_CONTENT_RETENTION): subjects and bodies are in the database "+
			"and shown to admin keys", "for", cfg.ContentRetention.String())
	}

	keyring := keys.New(db).WithMax(cfg.MaxKeys)
	// One of each service, for the API and the console alike: so that
	// neither can be given a rule the other lacks.
	endpoints := webhook.NewService(db, poster, cfg.Webhook.AllowPrivate, cfg.Webhook.MaxEndpoints, cfg.Webhook.SecretOverlap, time.Now)
	suppressions := suppress.New(db)
	mailer := mail.New(db, drv, mail.Options{SendTimeout: cfg.SendTimeout,
		IdempotencyTTL: cfg.IdempotencyTTL, InDoubtWindow: cfg.InDoubtWindow, KeepContent: cfg.ContentRetention,
		MaxRecipients: cfg.MaxRecipients, MaxAttachments: attachmentsOf(cfg), MaxAttachmentBytes: cfg.MaxAttachmentBytes,
		MaxBatch: cfg.MaxBatch, BatchParallel: cfg.BatchParallel, BatchTime: cfg.BatchTimeout, Stopping: ctx.Done()}, log)
	counted := measured{sent: stats.New(db, 0), endpoints: endpoints, driver: drv, version: version, log: log,
		parts: map[string]func() bool{"events": eventsHealth(reader), "webhooks": hooks.Healthy, "pruning": pruner.Healthy}}
	var handler http.Handler = api.New(api.Deps{
		Requests: &metrics.Requests{}, Gauges: counted.gauges,
		Mail: mailer, Keys: keyring, Suppress: suppressions,
		Webhooks: endpoints, Ping: db.Ping, Captured: captured, Quota: quotaOf(drv),
		Driver: drv.Name(), Version: version, MaxBodyBytes: cfg.MaxBodyBytes, MaxEmailBytes: emailBytes(cfg), MaxUploadBytes: cfg.MaxUploadBytes, Log: log,
		// A batch starts emails for so long, and the last of them may
		// take a whole send and its record.
		BatchTime: cfg.BatchTimeout + cfg.SendTimeout + 20*time.Second,
		Events:    eventsHealth(reader), Sending: hooks.Healthy, Pruning: pruner.Healthy,
	})
	if cfg.Console.On {
		// The console is served beside the API, under its own path.
		both := http.NewServeMux()
		eventsOff := "Not read: no queue of events is set. An email's record stops at \"sent\", and a bounce is never known."
		switch {
		case captured != nil:
			eventsOff = "None to read: this driver keeps emails and sends them to nobody."
		case neverTold:
			eventsOff = "None to read: a mail server spoken to by SMTP tells nothing of an email after it took it. " +
				"An email's record stops at \"sent\", and a bounce is never known."
		}
		pages := console.New(console.Deps{Keys: keyring, Mail: mailer, Suppress: suppressions, Webhooks: endpoints,
			Domains: domains.New(db, dnscheck.New(cfg.DNSTimeout, cfg.DNSServer), drv.Name(), recordsFor(drv.Name())), Sessions: session.New(db, cfg.Console.Session),
			Events: eventsHealth(reader), Sending: hooks.Healthy, Pruning: pruner.Healthy, EventsOff: eventsOff,
			Driver: drv.Name(), Version: version, PlainHTTP: cfg.Console.PlainHTTP,
			PauseAfter: cfg.Webhook.PauseAfter, Stats: stats.New(db, cfg.Console.BounceWarn), Zone: cfg.Console.Zone,
			Quota: consoleQuota(drv), Log: log})
		both.Handle(console.Root, pages)
		both.Handle(console.Root+"/", pages)
		both.Handle("/", handler)
		handler = both
		if cfg.Console.PlainHTTP {
			log.Warn("the console's session cookie may travel over plain http (CAIRNPOST_CONSOLE_PLAIN_HTTP)")
		}
	}
	srv := &http.Server{Addr: cfg.Addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: readTimeout,
		// The slowest answers are a send and the try-out of a webhook
		// endpoint: each must be able to run to its own timeout. The
		// time to answer is counted from the request's first line, so
		// the time a body may take to arrive is part of it: a send that
		// arrived slowly must still be answered once it is made.
		WriteTimeout: readTimeout + max(cfg.SendTimeout, cfg.Webhook.Timeout) + 20*time.Second, IdleTimeout: 2 * time.Minute}

	failed := make(chan error, 1)
	go func() { failed <- srv.ListenAndServe() }()
	log.Info("cairnpost is listening", "addr", cfg.Addr, "driver", drv.Name(), "version", version)
	select {
	case err := <-failed:
		return fmt.Errorf("the server stopped: %w", err)
	case <-ctx.Done():
	}
	log.Info("stopping: letting requests in flight finish")
	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.SendTimeout+5*time.Second)
	defer cancel()
	err = srv.Shutdown(stopCtx)
	<-reading // the reader finishes the batch it holds
	<-sending // and the webhooks in flight are settled or handed back
	<-pruning // and the pruner stops after the delete it is in
	return err
}

// capturedEmails is what the capture driver holds, as the API shows it.
func capturedEmails(d *capture.Driver) []api.CapturedEmail {
	var out []api.CapturedEmail
	for _, c := range d.Held() {
		out = append(out, api.CapturedEmail{ID: c.ID, At: c.At, From: c.From, FromName: c.FromName, To: c.To, Cc: c.Cc, Bcc: c.Bcc,
			ReplyTo: c.ReplyTo, Subject: c.Subject, HTML: c.HTML, Text: c.Text, Headers: c.Headers, Attachments: filesOf(c.Files)})
	}
	return out
}

// quotaOf is how the API asks the driver for its sending allowance; nil for
// a driver whose upstream has none to tell.
func quotaOf(d driver.Driver) func(context.Context) (api.Quota, error) {
	limited, ok := d.(driver.Limited)
	if !ok {
		return nil
	}
	return func(ctx context.Context) (api.Quota, error) {
		q, err := limited.Quota(ctx)
		if err != nil {
			return api.Quota{}, err
		}
		out := api.Quota{Limit: q.Limit, Per: q.Per, Used: q.Used, Over: q.Over}
		if !q.ResetsAt.IsZero() {
			out.ResetsAt = &q.ResetsAt
		}
		return out, nil
	}
}

// consoleQuota is the same question, asked of the same driver, for the
// console's first page.
func consoleQuota(d driver.Driver) func(context.Context) (console.Quota, error) {
	limited, ok := d.(driver.Limited)
	if !ok {
		return nil
	}
	return func(ctx context.Context) (console.Quota, error) {
		q, err := limited.Quota(ctx)
		return console.Quota{Limit: q.Limit, Used: q.Used, Per: q.Per, Over: q.Over, ResetsAt: q.ResetsAt}, err
	}
}

// eventsHealth is how /health says whether delivery events are being read;
// nil when none are read at all.
func eventsHealth(r *events.Reader) func() bool {
	if r == nil {
		return nil
	}
	return r.Healthy
}
