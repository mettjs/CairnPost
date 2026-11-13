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
	// through. Default 2m.
	InDoubtWindow time.Duration
	// SendTimeout is CAIRNPOST_SEND_TIMEOUT: the most one call to the
	// upstream may take. Default 10s.
	SendTimeout time.Duration
	// MaxBodyBytes is CAIRNPOST_MAX_BODY_BYTES: the largest request body
	// accepted. Default 1 MiB.
	MaxBodyBytes int64
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
	// PruneEvery is CAIRNPOST_PRUNE_EVERY: how long the pruner rests
	// between passes. Default 1h.
	PruneEvery time.Duration
	// Cloudflare is the cloudflare driver's settings.
	Cloudflare Cloudflare
	// Webhook is how events are delivered to endpoints.
	Webhook Webhook
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

// Drivers are the values CAIRNPOST_DRIVER takes.
var Drivers = []string{"capture", "cloudflare"}

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
	c.Retention, err = retention(getenv, c.IdempotencyTTL, retries)
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
