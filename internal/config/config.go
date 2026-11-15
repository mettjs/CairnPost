// Package config reads CairnPost's settings from the environment. Only the
// composition root imports it.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is every setting, each with its variable, default and purpose.
type Config struct {
	// PostgresDSN is CAIRNPOST_POSTGRES_DSN. Required.
	PostgresDSN string
	// Addr is CAIRNPOST_ADDR, where the HTTP API listens. Default ":8080".
	Addr string
	// Driver is CAIRNPOST_DRIVER, the upstream that delivers. Required, with
	// no default: a deployment that forgot it must not quietly keep its mail.
	Driver string
	// IdempotencyTTL is CAIRNPOST_IDEMPOTENCY_TTL: how long an
	// Idempotency-Key is remembered. Default 24h.
	IdempotencyTTL time.Duration
	// InDoubtWindow is CAIRNPOST_IN_DOUBT_WINDOW: how long a send whose
	// answer was lost blocks a retry of its key before the retry is let
	// through. Default 2m. It is also, through AbandonedAfter, how long
	// a send may stay "sending" before it is recorded as unknown.
	InDoubtWindow time.Duration
	// SendTimeout is CAIRNPOST_SEND_TIMEOUT: the most one call to the
	// upstream may take. Default 10s.
	SendTimeout time.Duration
	// MaxBodyBytes is CAIRNPOST_MAX_BODY_BYTES: the largest request body
	// accepted. Default 1 MiB.
	MaxBodyBytes int64
	// MaxRecipients is CAIRNPOST_MAX_RECIPIENTS: how many addresses one
	// email may go to, counting "to", "cc" and "bcc". Default 50, which
	// is what Cloudflare takes.
	MaxRecipients int
	// MaxBatch is CAIRNPOST_MAX_BATCH: how many emails one batch may
	// hold. BatchParallel is CAIRNPOST_BATCH_PARALLEL: how many of them
	// are sent at one time. BatchTimeout is CAIRNPOST_BATCH_TIMEOUT: how
	// long a batch goes on starting emails. Defaults 100, 4 and 1m.
	MaxBatch      int
	BatchParallel int
	BatchTimeout  time.Duration
	// ContentFiles is CAIRNPOST_CONTENT_FILES: whether an email's files
	// are kept with what it said, where that is kept. Default on.
	ContentFiles bool
	// Schedule is how emails asked for ahead of time are kept and sent.
	Schedule Schedule
	// MaxAttachments is CAIRNPOST_MAX_ATTACHMENTS: how many files one
	// email may carry, 0 for none. MaxAttachmentBytes is
	// CAIRNPOST_MAX_ATTACHMENT_BYTES: how large they may be in all.
	MaxAttachments     int
	MaxAttachmentBytes int64
	// MaxUploadBytes is CAIRNPOST_MAX_UPLOAD_BYTES: how large the sends
	// with files being taken at one time may be in all. A send over it
	// is told to come back: what guards the server's memory.
	MaxUploadBytes int64
	// CaptureKeep is CAIRNPOST_CAPTURE_KEEP: how many emails the capture
	// driver holds in memory. Default 100.
	CaptureKeep int
	// EventsPoll is CAIRNPOST_EVENTS_POLL: how long the reader of delivery
	// events rests after finding none. Default 5s.
	EventsPoll time.Duration
	// EventsGrace is CAIRNPOST_EVENTS_GRACE: how long an event about an
	// email with no record is kept waiting for that record before it is
	// let go. Default 2m.
	EventsGrace time.Duration
	// EventsBatch is CAIRNPOST_EVENTS_BATCH: how many events are taken at
	// once, 100 at most. Default 50.
	EventsBatch int
	// EventsLease is CAIRNPOST_EVENTS_LEASE: how long a batch taken stays
	// this reader's, and so how long it has to record it. Default 1m.
	EventsLease time.Duration
	// EventsRetry is CAIRNPOST_EVENTS_RETRY: how long an event handed back
	// waits before it is given again. Default 30s.
	EventsRetry time.Duration
	// Retention is CAIRNPOST_RETENTION: how long an email's record is kept
	// (who, to whom, what became of it, its events and the webhooks sent
	// for them) after the last thing that happened to it. In days ("30d")
	// or as a duration, 7d at least; "off" keeps records for ever, and is
	// 0 here. Default 30d.
	Retention time.Duration
	// ContentRetention is CAIRNPOST_CONTENT_RETENTION: how long what an
	// email said (subject, bodies, headers) is kept after it is sent, for
	// whoever runs CairnPost to see what went out. 30d at most and never
	// longer than Retention. Default "off", which is 0: nothing is kept.
	ContentRetention time.Duration
	// DNSTimeout is CAIRNPOST_DNS_TIMEOUT: the most one check of a
	// domain's DNS records may take. Default 6s.
	DNSTimeout time.Duration
	// DNSServer is CAIRNPOST_DNS_SERVER: the one DNS server those checks
	// ask, as "address:port"; "" for the system's resolver, the default.
	DNSServer string
	// CountsRetention is CAIRNPOST_COUNTS_RETENTION: how long the counts
	// of what was sent each hour are kept, whatever Retention is. In days
	// ("62d") or as a duration, 62d at least; "off" keeps them for ever,
	// and is 0. Default 62d.
	CountsRetention time.Duration
	// PruneEvery is CAIRNPOST_PRUNE_EVERY: how long the pruner rests
	// between passes. Default 1h.
	PruneEvery time.Duration
	// Cloudflare is the cloudflare driver's settings.
	Cloudflare Cloudflare
	// SMTP is the smtp driver's settings.
	SMTP SMTP
	// Webhook is how events are delivered to endpoints.
	Webhook Webhook
	// Console is the management console.
	Console Console
	// MaxKeys is CAIRNPOST_MAX_KEYS: how many live API keys one sending
	// domain may have, and how many bound to none. Default 200.
	MaxKeys int
	// Inbound is how emails that come are taken and kept.
	Inbound Inbound
	// MaxTemplates is CAIRNPOST_MAX_TEMPLATES: how many templates one
	// sending domain may have, and how many may serve every domain.
	// Default 200.
	MaxTemplates int
}

// Webhook is how events are delivered to the endpoints that hear about
// them.
type Webhook struct {
	// Timeout is CAIRNPOST_WEBHOOK_TIMEOUT: the most one try may take.
	// Default 10s.
	Timeout time.Duration
	// Retries is CAIRNPOST_WEBHOOK_RETRIES: the waits before each further
	// try, separated by commas. A webhook is tried once, then once after
	// each wait, then given up. Default 5s,5m,30m,2h,5h,10h,10h.
	Retries []time.Duration
	// Workers is CAIRNPOST_WEBHOOK_WORKERS: how many webhooks are being
	// sent at once, to all endpoints together. Default 8.
	Workers int
	// PerEndpoint is CAIRNPOST_WEBHOOK_PER_ENDPOINT: how many are being
	// sent at once to one endpoint. Under Workers, it keeps an endpoint
	// that hangs from holding up the others. Default 2.
	PerEndpoint int
	// MaxEndpoints is CAIRNPOST_WEBHOOK_MAX_ENDPOINTS: how many endpoints
	// may hear about one sending domain, and how many about all. Every
	// event is checked against them. Default 20.
	MaxEndpoints int
	// Poll is CAIRNPOST_WEBHOOK_POLL: how long the sender rests when
	// nothing is due. Default 2s.
	Poll time.Duration
	// PauseAfter is CAIRNPOST_WEBHOOK_PAUSE_AFTER: how long an endpoint
	// may fail every try before it is paused, in days ("5d") or as a
	// duration, 1h at least; "off" never pauses, and is 0. Default 5d.
	PauseAfter time.Duration
	// SecretOverlap is CAIRNPOST_WEBHOOK_SECRET_OVERLAP: how long, after
	// an endpoint's signing secret is changed, webhooks are signed with
	// the one before as well. In days or as a duration, 7d at most;
	// "off" ends the old one at once, and is 0. Default 24h.
	SecretOverlap time.Duration
	// AllowPrivate is CAIRNPOST_WEBHOOK_ALLOW_PRIVATE: let an endpoint be
	// plain http, and at an address that is not on the public internet.
	// For an application on the same private network. Default false.
	AllowPrivate bool
}

// Cloudflare is how the cloudflare driver signs in. Both are required when
// it is the driver.
type Cloudflare struct {
	// AccountID is CAIRNPOST_CLOUDFLARE_ACCOUNT_ID, the account whose
	// sending domains are used.
	AccountID string
	// APIToken is CAIRNPOST_CLOUDFLARE_API_TOKEN, a token with the Email
	// Sending permission. A secret: read here, handed to the driver, and
	// written nowhere.
	APIToken string
	// QueueID is CAIRNPOST_CLOUDFLARE_QUEUE_ID: the Queue an Email Sending
	// event subscription publishes to, read by HTTP pull. Optional: without
	// it delivery events are not read, and an email's record stops at
	// "sent". The token then also needs the Queues permission.
	QueueID string
}

// AbandonedAfter is how long a send may stay "sending" before it is taken
// to have been abandoned by a server that stopped in the middle of it: the
// in-doubt window, and never less than a send and the writing down of its
// answer can take.
func (c Config) AbandonedAfter() time.Duration {
	return max(c.InDoubtWindow, c.SendTimeout+time.Minute)
}

// Drivers are the values CAIRNPOST_DRIVER takes.
var Drivers = []string{"capture", "cloudflare", "smtp"}

// Load reads the environment through getenv and refuses what cannot run.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		PostgresDSN: getenv("CAIRNPOST_POSTGRES_DSN"),
		Addr:        or(getenv("CAIRNPOST_ADDR"), ":8080"),
		Driver:      getenv("CAIRNPOST_DRIVER"),
	}
	if c.PostgresDSN == "" {
		return c, fmt.Errorf("config: set CAIRNPOST_POSTGRES_DSN to the database's address")
	}
	if !known(c.Driver) {
		return c, fmt.Errorf("config: set CAIRNPOST_DRIVER to one of %v (got %q)", Drivers, c.Driver)
	}
	var err error
	if c.IdempotencyTTL, err = duration(getenv, "CAIRNPOST_IDEMPOTENCY_TTL", 24*time.Hour); err != nil {
		return c, err
	}
	if c.InDoubtWindow, err = duration(getenv, "CAIRNPOST_IN_DOUBT_WINDOW", 2*time.Minute); err != nil {
		return c, err
	}
	if c.SendTimeout, err = duration(getenv, "CAIRNPOST_SEND_TIMEOUT", 10*time.Second); err != nil {
		return c, err
	}
	if c.MaxBodyBytes, err = number(getenv, "CAIRNPOST_MAX_BODY_BYTES", 1<<20); err != nil {
		return c, err
	}
	most, err := number(getenv, "CAIRNPOST_MAX_RECIPIENTS", 50)
	if err != nil {
		return c, err
	}
	if most < 1 || most > 1000 {
		return c, fmt.Errorf("config: CAIRNPOST_MAX_RECIPIENTS must be between 1 and 1000 (got %d)", most)
	}
	c.MaxRecipients = int(most)
	for _, n := range []struct {
		name          string
		into          *int
		fallback, top int64
	}{{"CAIRNPOST_MAX_BATCH", &c.MaxBatch, 100, 1000}, {"CAIRNPOST_BATCH_PARALLEL", &c.BatchParallel, 4, 64}} {
		v, err := number(getenv, n.name, n.fallback)
		if err != nil {
			return c, err
		}
		if v < 1 || v > n.top {
			return c, fmt.Errorf("config: %s must be between 1 and %d (got %d)", n.name, n.top, v)
		}
		*n.into = int(v)
	}
	if c.BatchTimeout, err = duration(getenv, "CAIRNPOST_BATCH_TIMEOUT", time.Minute); err != nil {
		return c, err
	}
	if c.BatchTimeout > 10*time.Minute {
		return c, fmt.Errorf("config: CAIRNPOST_BATCH_TIMEOUT must be at most 10m (got %s)", c.BatchTimeout)
	}
	if c.ContentFiles, err = flag(getenv, "CAIRNPOST_CONTENT_FILES", true); err != nil {
		return c, err
	}
	if c.Schedule, err = schedule(getenv); err != nil {
		return c, err
	}
	// 0 is a setting here, "no files", so it is read apart from the
	// numbers that must be positive.
	c.MaxAttachments = 10
	if v := getenv("CAIRNPOST_MAX_ATTACHMENTS"); v != "" {
		if c.MaxAttachments, err = strconv.Atoi(v); err != nil || c.MaxAttachments < 0 || c.MaxAttachments > 100 {
			return c, fmt.Errorf("config: CAIRNPOST_MAX_ATTACHMENTS must be a whole number between 0 and 100 (got %q)", v)
		}
	}
	if c.MaxAttachmentBytes, err = number(getenv, "CAIRNPOST_MAX_ATTACHMENT_BYTES", 10<<20); err != nil {
		return c, err
	}
	if c.MaxAttachmentBytes < 1 || c.MaxAttachmentBytes > 64<<20 {
		return c, fmt.Errorf("config: CAIRNPOST_MAX_ATTACHMENT_BYTES must be between 1 and %d (got %d)", 64<<20, c.MaxAttachmentBytes)
	}
	if c.MaxUploadBytes, err = number(getenv, "CAIRNPOST_MAX_UPLOAD_BYTES", 128<<20); err != nil {
		return c, err
	}
	if c.MaxUploadBytes < c.MaxAttachmentBytes*2 {
		return c, fmt.Errorf("config: CAIRNPOST_MAX_UPLOAD_BYTES (%d) must be at least twice CAIRNPOST_MAX_ATTACHMENT_BYTES (%d), "+
			"or one email with files could not be taken", c.MaxUploadBytes, c.MaxAttachmentBytes)
	}
	// A retry must not be let through while the first try can still be
	// answered, and a key must outlive the wait for that answer.
	if c.InDoubtWindow <= c.SendTimeout {
		return c, fmt.Errorf("config: CAIRNPOST_IN_DOUBT_WINDOW (%s) must be longer than CAIRNPOST_SEND_TIMEOUT (%s), "+
			"or a retry could be sent while the first try is still being answered", c.InDoubtWindow, c.SendTimeout)
	}
	if c.IdempotencyTTL <= c.InDoubtWindow {
		return c, fmt.Errorf("config: CAIRNPOST_IDEMPOTENCY_TTL (%s) must be longer than CAIRNPOST_IN_DOUBT_WINDOW (%s), "+
			"or a key would be forgotten while its send is still in doubt", c.IdempotencyTTL, c.InDoubtWindow)
	}
	c.Cloudflare = Cloudflare{
		AccountID: strings.TrimSpace(getenv("CAIRNPOST_CLOUDFLARE_ACCOUNT_ID")),
		APIToken:  strings.TrimSpace(getenv("CAIRNPOST_CLOUDFLARE_API_TOKEN")),
		QueueID:   strings.TrimSpace(getenv("CAIRNPOST_CLOUDFLARE_QUEUE_ID")),
	}
	if c.SMTP, err = smtpOf(getenv, c.Driver == "smtp"); err != nil {
		return c, err
	}
	if c.EventsPoll, err = duration(getenv, "CAIRNPOST_EVENTS_POLL", 5*time.Second); err != nil {
		return c, err
	}
	if c.EventsGrace, err = duration(getenv, "CAIRNPOST_EVENTS_GRACE", 2*time.Minute); err != nil {
		return c, err
	}
	if c.EventsLease, err = duration(getenv, "CAIRNPOST_EVENTS_LEASE", time.Minute); err != nil {
		return c, err
	}
	if c.EventsRetry, err = duration(getenv, "CAIRNPOST_EVENTS_RETRY", 30*time.Second); err != nil {
		return c, err
	}
	batch, err := number(getenv, "CAIRNPOST_EVENTS_BATCH", 50)
	if err != nil {
		return c, err
	}
	if batch > 100 {
		return c, fmt.Errorf("config: CAIRNPOST_EVENTS_BATCH must be at most 100 (got %d)", batch)
	}
	c.EventsBatch = int(batch)
	if c.Driver == "cloudflare" && (c.Cloudflare.AccountID == "" || c.Cloudflare.APIToken == "") {
		return c, fmt.Errorf("config: the cloudflare driver needs CAIRNPOST_CLOUDFLARE_ACCOUNT_ID and " +
			"CAIRNPOST_CLOUDFLARE_API_TOKEN (a token with the Email Sending permission)")
	}
	keep, err := number(getenv, "CAIRNPOST_CAPTURE_KEEP", 100)
	if err != nil {
		return c, err
	}
	c.CaptureKeep = int(keep)
	if c.DNSTimeout, err = DNSTimeout(getenv); err != nil {
		return c, err
	}
	if c.DNSServer, err = DNSServer(getenv); err != nil {
		return c, err
	}
	if c.PruneEvery, err = duration(getenv, "CAIRNPOST_PRUNE_EVERY", time.Hour); err != nil {
		return c, err
	}
	if c.Webhook, err = webhook(getenv); err != nil {
		return c, err
	}
	var retries time.Duration
	for _, wait := range c.Webhook.Retries {
		retries += wait
	}
	if c.Retention, err = retention(getenv, c.IdempotencyTTL, retries); err != nil {
		return c, err
	}
	if c.CountsRetention, err = countsRetention(getenv); err != nil {
		return c, err
	}
	if c.ContentRetention, err = contentRetention(getenv, c.Retention); err != nil {
		return c, err
	}
	if c.Console, err = console(getenv); err != nil {
		return c, err
	}
	if c.MaxKeys, err = MaxKeys(getenv); err != nil {
		return c, err
	}
	if c.Inbound, err = inbound(getenv); err != nil {
		return c, err
	}
	if c.Inbound.Secret != "" && c.MaxUploadBytes < c.Inbound.MaxBytes {
		return c, fmt.Errorf("config: CAIRNPOST_MAX_UPLOAD_BYTES (%d) must be at least CAIRNPOST_INBOUND_MAX_BYTES (%d), "+
			"or one email that comes could not be taken", c.MaxUploadBytes, c.Inbound.MaxBytes)
	}
	templatesMost, err := number(getenv, "CAIRNPOST_MAX_TEMPLATES", 200)
	if err == nil && templatesMost > 100000 {
		err = fmt.Errorf("config: CAIRNPOST_MAX_TEMPLATES must be at most 100000 (got %d)", templatesMost)
	}
	c.MaxTemplates = int(templatesMost)
	return c, err
}

func known(driver string) bool {
	for _, d := range Drivers {
		if d == driver {
			return true
		}
	}
	return false
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func duration(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive duration such as 30s or 2h (got %q)", name, v)
	}
	return d, nil
}

func number(getenv func(string) string, name string, fallback int64) (int64, error) {
	v := getenv(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive whole number (got %q)", name, v)
	}
	return n, nil
}
