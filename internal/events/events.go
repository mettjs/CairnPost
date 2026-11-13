// Package events reads what an upstream says became of the emails it took,
// and records it.
package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/store"
)

// Options are the reader's settings.
type Options struct {
	// Poll is how long the reader rests after finding nothing.
	Poll time.Duration
	// Batch is how many events one pass takes.
	Batch int
	// Lease is how long a batch taken stays ours. A pass must finish
	// within it, and is given that long and no longer.
	Lease time.Duration
	// Retry is how long an event handed back waits before it is given
	// again.
	Retry time.Duration
	// Grace is how long an event about an email nobody knows is handed
	// back before it is let go: an event can outrun the record of the send
	// it is about.
	Grace time.Duration
	// KeepDetails is how long after an email was sent a receiving
	// server's sentence about it is kept; 0 keeps none. It is the time
	// the operator keeps what emails say.
	KeepDetails time.Duration
}

// kindOf maps each kind a driver reports to the kind the store keeps. It
// is the one place the two vocabularies meet.
var kindOf = map[string]string{
	driver.Delivered: store.EventDelivered, driver.Deferred: store.EventDeferred,
	driver.Bounced: store.EventBounced, driver.Complained: store.EventComplained,
	driver.Failed: store.EventFailed,
}

// Reader records an upstream's events.
type Reader struct {
	store  store.Events
	ping   func(context.Context) error
	source driver.Source
	driver string
	opt    Options
	now    func() time.Time
	log    *slog.Logger
	// failing is set while passes fail, for Healthy.
	failing atomic.Bool
}

// New returns a reader of source, whose events are about emails driverName
// sent. ping says whether the store can be reached: events are not taken
// while it cannot.
func New(s store.Events, ping func(context.Context) error, source driver.Source, driverName string,
	opt Options, now func() time.Time, log *slog.Logger) *Reader {
	return &Reader{store: s, ping: ping, source: source, driver: driverName, opt: opt, now: now, log: log}
}

// Healthy reports whether the last pass went through.
func (r *Reader) Healthy() bool { return !r.failing.Load() }

// longestRest caps how long the reader rests between passes that fail.
const longestRest = 5 * time.Minute

// Run reads until the context ends, then finishes the pass it is in. A
// pass that fails is tried again after a rest that doubles up to five
// minutes, or the wait the upstream asked for: what was not settled is
// kept by the upstream, and a queue is not hammered while it refuses.
func (r *Reader) Run(ctx context.Context) {
	rest := r.opt.Poll
	for ctx.Err() == nil {
		// A pass, once begun, is not cut short by a shutdown: an event
		// recorded and not acknowledged would be handed over again.
		passCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.opt.Lease)
		taken, err := r.Pass(passCtx)
		cancel()
		var throttled *driver.Throttled
		switch {
		case err == nil:
			if r.failing.Swap(false) {
				r.log.Info("events: reading again")
			}
			rest = r.opt.Poll
			if taken == r.opt.Batch {
				continue // a full batch: more are likely waiting
			}
		default:
			r.failing.Store(true)
			if errors.As(err, &throttled) && throttled.After > rest {
				rest = throttled.After
			}
			r.log.Error("events: delivery events are not being read; trying again", "error", err, "in", rest.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(rest):
		}
		if err != nil {
			rest = min(rest*2, longestRest)
		}
	}
}

// Pass takes one batch, records it and settles it, and answers how many
// items it took. An event is acknowledged only after it is recorded, or
// found to be nothing to record. An upstream gives an event only so many
// times, so nothing is taken while the store cannot be reached, and an
// event that could not be recorded is handed back rather than left to lapse.
func (r *Reader) Pass(ctx context.Context) (int, error) {
	if err := r.ping(ctx); err != nil {
		return 0, fmt.Errorf("events: the store cannot be reached, so no event is taken: %w", err)
	}
	items, err := r.source.Take(ctx, r.opt.Batch, r.opt.Lease)
	if err != nil || len(items) == 0 {
		return 0, err
	}
	var (
		done, back               []string
		kept, unnamed, strangers int
		notOurs, lastTry         int
		suppressing              int
		failed                   error
	)
	for _, item := range items {
		e := item.Event
		kind, known := kindOf[e.Kind]
		switch {
		case !item.Readable && e.Kind == "":
			notOurs++ // not an event about an email at all
			done = append(done, item.Lease)
			continue
		case !item.Readable || !known:
			unnamed++ // an email's event that does not name its email
			done = append(done, item.Lease)
			continue
		}
		if e.At.IsZero() {
			e.At = r.now()
		}
		record := store.Event{Driver: r.driver, UpstreamEventID: e.ID,
			UpstreamID: e.UpstreamID, Kind: kind, Permanent: e.Permanent, Code: e.Code, Reason: e.Reason, At: e.At,
			Sender: e.Sender, Recipient: e.Recipient}
		if r.opt.KeepDetails != 0 {
			// Only where what emails say is kept, whatever the source read.
			record.Detail, record.KeepDetail = e.Detail, r.opt.KeepDetails
		}
		outcome, err := r.store.RecordEvent(ctx, record)
		_, suppresses := store.SuppressionFor(kind, e.Permanent, e.Code)
		switch {
		case err != nil:
			failed = err
			back = append(back, item.Lease)
			if item.Attempts >= 2 {
				lastTry++
			}
		case outcome == store.NoMessage && r.now().Sub(e.At) < r.opt.Grace:
			back = append(back, item.Lease)
		case outcome == store.NoMessage:
			strangers++
			if suppresses && e.Sender != "" && e.Recipient != "" {
				suppressing++
			}
			done = append(done, item.Lease)
		default:
			if outcome == store.Kept {
				kept++
				if suppresses {
					suppressing++
				}
			}
			done = append(done, item.Lease)
		}
	}
	if err := r.source.Settle(ctx, done, back, r.opt.Retry); err != nil && failed == nil {
		failed = err
	}
	// Counts only: an event's addresses stay out of the log.
	if kept+strangers+notOurs > 0 {
		r.log.Info("events: a batch was read", "recorded", kept, "about_emails_not_ours", strangers,
			"not_email_events", notOurs, "handed_back", len(back), "suppressing", suppressing)
	}
	if unnamed > 0 {
		r.log.Warn("events: events that do not name their email were dropped; the upstream's format may have changed",
			"dropped", unnamed)
	}
	if lastTry > 0 {
		r.log.Warn("events: events could not be recorded on what may be their last delivery; "+
			"raise the queue consumer's max retries so an outage loses none", "events", lastTry)
	}
	return len(items), failed
}
