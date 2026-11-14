package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/stats"
	"github.com/mettjs/cairnpost/internal/store/memstore"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// slow is a mail provider whose allowance never comes.
type slow struct{ driver.Driver }

func (slow) Name() string { return "slow" }
func (slow) Quota(ctx context.Context) (driver.Quota, error) {
	<-ctx.Done()
	return driver.Quota{}, ctx.Err()
}

// A mail provider that does not answer costs the scrape its allowance
// alone: the other numbers are still said, in the time one read has.
func TestASlowProviderCostsOnlyItsOwnNumber(t *testing.T) {
	db := memstore.New()
	m := measured{sent: stats.New(db, 0), endpoints: webhook.NewService(db, nil, true, 20, time.Hour, time.Now),
		driver: slow{}, version: "test", log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		parts: map[string]func() bool{"webhooks": func() bool { return true }}}
	began := time.Now()
	names := map[string]int{}
	for _, s := range m.gauges(t.Context(), "example.com") {
		names[s.Name]++
	}
	if took := time.Since(began); took > gaugeWait+2*time.Second {
		t.Fatalf("it took %v", took)
	}
	if names["cairnpost_provider_quota"] != 0 || names["cairnpost_emails_last_day"] == 0 || names["cairnpost_webhook_endpoints"] != 3 ||
		names["cairnpost_part_up"] != 1 || names["cairnpost_build_info"] != 1 || names["cairnpost_database_up"] != 0 {
		t.Fatalf("%v", names)
	}
}
