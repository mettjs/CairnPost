package main

import (
	"context"
	"log/slog"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/metrics"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// measured is what GET /metrics reads beside the requests it counted.
type measured struct {
	ping      func(context.Context) error
	counts    store.Counts
	endpoints *webhook.Service
	driver    driver.Driver
	version   string
	// parts report whether each part that runs beside the API is
	// working; a part that is off is not in it.
	parts map[string]func() bool
	log   *slog.Logger
}

// gauges answers how the server stands, in numbers. A number that cannot
// be read is left out and the reason logged: the rest is still answered.
func (m measured) gauges(ctx context.Context) []metrics.Sample {
	out := []metrics.Sample{
		{Name: "cairnpost_build_info", Help: "The version running and the mail provider it sends through.", Kind: metrics.Gauge,
			Labels: [][2]string{{"version", m.version}, {"driver", m.driver.Name()}}, Value: 1},
		{Name: "cairnpost_database_up", Help: "Whether the database answers.", Kind: metrics.Gauge, Value: metrics.Up(m.ping(ctx) == nil)},
	}
	for _, part := range []string{"events", "webhooks", "pruning"} {
		if working := m.parts[part]; working != nil {
			out = append(out, metrics.Sample{Name: "cairnpost_part_up", Help: "Whether a part that runs beside the API is working.",
				Kind: metrics.Gauge, Labels: [][2]string{{"part", part}}, Value: metrics.Up(working())})
		}
	}
	out = append(out, m.emails(ctx)...)
	out = append(out, m.hooks(ctx)...)
	return append(out, m.quota(ctx)...)
}

// emails is how many emails of the last 24 hours are in each state.
func (m measured) emails(ctx context.Context) []metrics.Sample {
	rows, _, err := m.counts.SendCounts(ctx, "", 24)
	if err != nil {
		m.log.Error("metrics: the send counts could not be read", "error", err)
		return nil
	}
	in := map[string]int{}
	for _, row := range rows {
		for state, n := range row.In {
			in[state] += n
		}
	}
	var out []metrics.Sample
	for _, state := range store.States {
		out = append(out, metrics.Sample{Name: "cairnpost_emails_last_day", Help: "Emails begun in the last 24 hours, by the state they are in now.",
			Kind: metrics.Gauge, Labels: [][2]string{{"state", state}}, Value: float64(in[state])})
	}
	return out
}

// hooks is how many webhook endpoints stand in each state, and how many
// are failing every try.
func (m measured) hooks(ctx context.Context) []metrics.Sample {
	ends, err := m.endpoints.List(ctx, "")
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
	for _, state := range []string{webhook.StateOn, webhook.StateOff, webhook.StatePaused} {
		out = append(out, metrics.Sample{Name: "cairnpost_webhook_endpoints", Help: "Webhook endpoints, by how they stand.",
			Kind: metrics.Gauge, Labels: [][2]string{{"state", state}}, Value: float64(in[state])})
	}
	return append(out, metrics.Sample{Name: "cairnpost_webhook_endpoints_failing", Help: "Webhook endpoints that are on and have failed every try since their last answer.",
		Kind: metrics.Gauge, Value: float64(failing)})
}

// quota is the mail provider's allowance, where it has one to tell. The
// driver asks the provider no more often than it would for anyone.
func (m measured) quota(ctx context.Context) []metrics.Sample {
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
