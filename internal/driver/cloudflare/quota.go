package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// quotaFresh is how long an answer about the quota is reused: it is asked
// for by people and dashboards, and Cloudflare need not hear each of them.
const quotaFresh = 30 * time.Second

// quotaRetry is how long Cloudflare is left alone after it failed to
// answer about the quota: whoever asks meanwhile is told the same.
const quotaRetry = 10 * time.Second

type cache struct {
	mu   sync.Mutex
	at   time.Time
	held driver.Quota
	// failed is the last failure and when it was, for quotaRetry.
	failed   error
	failedAt time.Time
}

// Quota implements driver.Limited: the account's daily allowance and how
// much of it is used, as Cloudflare counts it. A recent answer is reused,
// and so, for a shorter time, is a recent failure.
func (d *Driver) Quota(ctx context.Context) (driver.Quota, error) {
	d.quota.mu.Lock()
	fresh := !d.quota.at.IsZero() && time.Since(d.quota.at) < quotaFresh
	held, failed := d.quota.held, d.quota.failed
	if time.Since(d.quota.failedAt) >= quotaRetry {
		failed = nil
	}
	d.quota.mu.Unlock()
	switch {
	case fresh:
		return held, nil
	case failed != nil:
		return driver.Quota{}, failed
	}
	q, err := d.readQuota(ctx)
	// A caller that left, or would not wait, is no failure of Cloudflare's.
	if err != nil && ctx.Err() == nil {
		d.quota.mu.Lock()
		d.quota.failed, d.quota.failedAt = err, time.Now()
		d.quota.mu.Unlock()
	}
	return q, err
}

// readQuota asks Cloudflare now, and keeps a good answer for Quota.
func (d *Driver) readQuota(ctx context.Context) (driver.Quota, error) {
	a, err := d.ask(ctx, http.MethodGet, "/email/sending/limits", nil)
	if err != nil {
		return driver.Quota{}, err
	}
	if err := a.refusal(); err != nil {
		return driver.Quota{}, err
	}
	var res struct {
		Quota *struct {
			Value int    `json:"value"`
			Unit  string `json:"unit"`
		} `json:"quota"`
		Usage struct {
			Sent      int  `json:"sent"`
			OverQuota bool `json:"over_quota"`
			// A string, read leniently: what it holds before the first
			// send of a period is not known.
			ResetsAt string `json:"resets_at"`
		} `json:"usage"`
	}
	if json.Unmarshal(a.result, &res) != nil || res.Quota == nil {
		return driver.Quota{}, errors.New("cloudflare: the limits could not be read")
	}
	q := driver.Quota{Limit: res.Quota.Value, Per: res.Quota.Unit, Used: res.Usage.Sent, Over: res.Usage.OverQuota}
	if at, err := time.Parse(time.RFC3339, res.Usage.ResetsAt); err == nil {
		q.ResetsAt = at
	}
	d.quota.mu.Lock()
	d.quota.held, d.quota.at, d.quota.failed = q, time.Now(), nil
	d.quota.mu.Unlock()
	return q, nil
}
