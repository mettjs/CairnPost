package main

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/metrics"
	"github.com/mettjs/cairnpost/internal/stats"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// measured is what GET /metrics reads beside the requests it counted.
type measured struct {
	sent      *stats.Service
	endpoints *webhook.Service
	driver    driver.Driver
	version   string
	// parts report whether each part that runs beside the API is
	// working; a part that is off is not in it.
	parts map[string]func() bool
	// waiting counts the emails that wait to be sent.
	waiting func(ctx context.Context, domain string) (int, error)
	// came counts the emails that came and are kept; nil where none are
	// taken.
	came func(ctx context.Context, domain string) (int, error)
	log  *slog.Logger
}

// gaugeWait is how long one of the reads behind the numbers may take.
// Each has its own time, and they run side by side: a slow mail
// provider does not cost the scrape its other numbers.
const gaugeWait = 3 * time.Second

// gauges answers how the server stands, in numbers: for a key bound to
// a sending domain, that domain's emails and endpoints. A number that
// cannot be read is left out and the reason logged: the rest is still
// answered. Whether the database answers is not among them: a scrape
// needs it to know the key, so a scrape that fails is that number.
func (m measured) gauges(ctx context.Context, domain string) []metrics.Sample {
	out := []metrics.Sample{
		{Name: "cairnpost_build_info", Help: "The version running and the mail provider it sends through.", Kind: metrics.Gauge,
			Labels: [][2]string{{"version", m.version}, {"driver", m.driver.Name()}}, Value: 1},
	}
	for _, part := range []string{"events", "webhooks", "pruning", "scheduled"} {
		if working := m.parts[part]; working != nil {
			out = append(out, metrics.Sample{Name: "cairnpost_part_up", Help: "Whether a part that runs beside the API is working.",
				Kind: metrics.Gauge, Labels: [][2]string{{"part", part}}, Value: metrics.Up(working())})
		}
	}
	reads := []func(context.Context, string) []metrics.Sample{m.emails, m.hooks, m.quota, m.scheduled, m.inbound}
	read := make([][]metrics.Sample, len(reads))
	var wg sync.WaitGroup
	for i, one := range reads {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, gaugeWait)
			defer cancel()
			// A read that panics is one number less, not the server's end.
			defer func() {
				if p := recover(); p != nil {
					m.log.Error("metrics: a read panicked", "panic", p)
				}
			}()
			read[i] = one(ctx, domain)
		})
	}
	wg.Wait()
	return append(out, slices.Concat(read...)...)
}

// emails is how many emails of the last day are in each state.
func (m measured) emails(ctx context.Context, domain string) []metrics.Sample {
	in, err := m.sent.LastDay(ctx, domain)
	if err != nil {
		m.log.Error("metrics: the send counts could not be read", "error", err)
		return nil
	}
	var out []metrics.Sample
	for _, state := range store.States {
		out = append(out, metrics.Sample{Name: "cairnpost_emails_last_day", Help: "Emails begun in the last 24 whole hours, by the state they are in now.",
			Kind: metrics.Gauge, Labels: [][2]string{{"state", state}}, Value: float64(in[state])})
	}
	return out
}

// inbound is how many emails that came are kept.
func (m measured) inbound(ctx context.Context, domain string) []metrics.Sample {
	if m.came == nil {
		return nil
	}
	n, err := m.came(ctx, domain)
	if err != nil {
		m.log.Error("metrics: the emails that came could not be counted", "error", err)
		return nil
	}
	return []metrics.Sample{{Name: "cairnpost_inbound_emails", Help: "Emails that came for a sending domain's addresses and are kept.",
		Kind: metrics.Gauge, Value: float64(n)}}
}

// scheduled is how many emails wait to be sent.
func (m measured) scheduled(ctx context.Context, domain string) []metrics.Sample {
	if m.waiting == nil {
		return nil
	}
	n, err := m.waiting(ctx, domain)
	if err != nil {
		m.log.Error("metrics: the emails waiting to be sent could not be counted", "error", err)
		return nil
	}
	return []metrics.Sample{{Name: "cairnpost_emails_scheduled", Help: "Emails asked for ahead of time that wait to be sent.",
		Kind: metrics.Gauge, Value: float64(n)}}
}

// hooks is how many webhook endpoints stand in each state, and how many
// are failing every try.
func (m measured) hooks(ctx context.Context, domain string) []metrics.Sample {
	ends, err := m.endpoints.List(ctx, domain)
	if err != nil {
		m.log.Error("metrics: the webhook endpoints could not be read", "error", err)
		return nil
	}
	in, failing := map[string]int{}, 0
	for _, e := range ends {
		state := webhook.StateOf(e)
		if in[state]++; state == webhook.StateOn && e.FailingSince != nil {
			failing++
		}
	}
	var out []metrics.Sample
	for _, state := range webhook.States {
		out = append(out, metrics.Sample{Name: "cairnpost_webhook_endpoints", Help: "Webhook endpoints, by how they stand.",
			Kind: metrics.Gauge, Labels: [][2]string{{"state", state}}, Value: float64(in[state])})
	}
	return append(out, metrics.Sample{Name: "cairnpost_webhook_endpoints_failing", Help: "Webhook endpoints that are on and have failed every try since their last answer.",
		Kind: metrics.Gauge, Value: float64(failing)})
}

// quota is the mail provider's allowance, where it has one to tell. The
// driver asks the provider no more often than it would for anyone.
func (m measured) quota(ctx context.Context, _ string) []metrics.Sample {
	limited, ok := m.driver.(driver.Limited)
	if !ok {
		return nil
	}
	q, err := limited.Quota(ctx)
	if err != nil {
		// The provider's own words are not logged.
		m.log.Warn("metrics: the mail provider's allowance could not be read")
		return nil
	}
	return []metrics.Sample{
		{Name: "cairnpost_provider_quota", Help: "How many emails the mail provider sends in its period.", Kind: metrics.Gauge, Value: float64(q.Limit)},
		{Name: "cairnpost_provider_quota_used", Help: "How many of them are used.", Kind: metrics.Gauge, Value: float64(q.Used)},
	}
}
