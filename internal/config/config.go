// Package config reads CairnPost's settings from the environment. Only the
// composition root imports it.
package config

import (
	"fmt"
	"strconv"
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
}

// Drivers are the values CAIRNPOST_DRIVER takes.
var Drivers = []string{"capture"}

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
	keep, err := number(getenv, "CAIRNPOST_CAPTURE_KEEP", 100)
	c.CaptureKeep = int(keep)
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
