package config

import (
	"fmt"
	"strings"
	"time"
)

// webhook reads the webhook settings.
func webhook(getenv func(string) string) (Webhook, error) {
	var (
		w   Webhook
		err error
	)
	if w.Timeout, err = duration(getenv, "CAIRNPOST_WEBHOOK_TIMEOUT", 10*time.Second); err != nil {
		return w, err
	}
	if w.Poll, err = duration(getenv, "CAIRNPOST_WEBHOOK_POLL", 2*time.Second); err != nil {
		return w, err
	}
	workers, err := number(getenv, "CAIRNPOST_WEBHOOK_WORKERS", 8)
	if err != nil {
		return w, err
	}
	perEndpoint, err := number(getenv, "CAIRNPOST_WEBHOOK_PER_ENDPOINT", min(2, workers))
	if err != nil {
		return w, err
	}
	if w.MaxEndpoints, err = MaxEndpoints(getenv); err != nil {
		return w, err
	}
	switch {
	case workers > 64:
		return w, fmt.Errorf("config: CAIRNPOST_WEBHOOK_WORKERS must be at most 64 (got %d)", workers)
	case perEndpoint > workers:
		return w, fmt.Errorf("config: CAIRNPOST_WEBHOOK_PER_ENDPOINT (%d) must be at most CAIRNPOST_WEBHOOK_WORKERS (%d)", perEndpoint, workers)
	}
	w.Workers, w.PerEndpoint = int(workers), int(perEndpoint)
	for _, wait := range strings.Split(or(getenv("CAIRNPOST_WEBHOOK_RETRIES"), "5s,5m,30m,2h,5h,10h,10h"), ",") {
		d, err := time.ParseDuration(strings.TrimSpace(wait))
		if err != nil || d <= 0 {
			return w, fmt.Errorf("config: CAIRNPOST_WEBHOOK_RETRIES must be waits separated by commas, "+
				"such as 5s,5m,30m (got %q)", getenv("CAIRNPOST_WEBHOOK_RETRIES"))
		}
		w.Retries = append(w.Retries, d)
	}
	if len(w.Retries) > 20 {
		return w, fmt.Errorf("config: CAIRNPOST_WEBHOOK_RETRIES must be at most 20 waits")
	}
	w.AllowPrivate, err = AllowPrivate(getenv)
	return w, err
}

// MaxEndpoints reads CAIRNPOST_WEBHOOK_MAX_ENDPOINTS on its own, as
// AllowPrivate does.
func MaxEndpoints(getenv func(string) string) (int, error) {
	n, err := number(getenv, "CAIRNPOST_WEBHOOK_MAX_ENDPOINTS", 20)
	if err == nil && n > 1000 {
		err = fmt.Errorf("config: CAIRNPOST_WEBHOOK_MAX_ENDPOINTS must be at most 1000 (got %d)", n)
	}
	return int(n), err
}

// AllowPrivate reads CAIRNPOST_WEBHOOK_ALLOW_PRIVATE on its own, for a
// command that needs that one setting and not a whole configuration.
func AllowPrivate(getenv func(string) string) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(getenv("CAIRNPOST_WEBHOOK_ALLOW_PRIVATE"))); v {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, fmt.Errorf("config: CAIRNPOST_WEBHOOK_ALLOW_PRIVATE must be true or false (got %q)", v)
	}
}
