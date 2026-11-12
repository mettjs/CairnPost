package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/config"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store/postgres"
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
	)
	switch cfg.Driver {
	case capture.Name:
		kept := capture.New(cfg.CaptureKeep, time.Now)
		drv, captured = kept, func() []api.CapturedEmail { return capturedEmails(kept) }
		log.Warn("the capture driver is in use: emails are kept in memory and sent to nobody")
	default:
		return fmt.Errorf("no driver is built for %q", cfg.Driver)
	}

	handler := api.New(api.Deps{
		Mail: mail.New(db, drv, mail.Options{SendTimeout: cfg.SendTimeout,
			IdempotencyTTL: cfg.IdempotencyTTL, InDoubtWindow: cfg.InDoubtWindow}, log),
		Keys: keys.New(db), Ping: db.Ping, Captured: captured,
		Driver: drv.Name(), Version: version, MaxBodyBytes: cfg.MaxBodyBytes, Log: log,
	})
	srv := &http.Server{Addr: cfg.Addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: cfg.SendTimeout + 20*time.Second, IdleTimeout: 2 * time.Minute}

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
	return srv.Shutdown(stopCtx)
}

// capturedEmails is what the capture driver holds, as the API shows it.
func capturedEmails(d *capture.Driver) []api.CapturedEmail {
	var out []api.CapturedEmail
	for _, c := range d.Held() {
		out = append(out, api.CapturedEmail{ID: c.ID, At: c.At, From: c.From, FromName: c.FromName, To: c.To,
			ReplyTo: c.ReplyTo, Subject: c.Subject, HTML: c.HTML, Text: c.Text, Headers: c.Headers})
	}
	return out
}
