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

type cache struct {
	mu   sync.Mutex
	at   time.Time
	held driver.Quota
}

// Quota implements driver.Limited: the account's daily allowance and how
// much of it is used, as Cloudflare counts it. A recent answer is reused.
func (d *Driver) Quota(ctx context.Context) (driver.Quota, error) {
	d.quota.mu.Lock()
	fresh := !d.quota.at.IsZero() && time.Since(d.quota.at) < quotaFresh
	held := d.quota.held
	d.quota.mu.Unlock()
	if fresh {
		return held, nil
	}
	return d.readQuota(ctx)
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
	d.quota.held, d.quota.at = q, time.Now()
	d.quota.mu.Unlock()
	return q, nil
}
