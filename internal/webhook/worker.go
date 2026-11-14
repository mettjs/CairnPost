package webhook

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// Options are the worker's settings.
type Options struct {
	// Poll is how long the worker rests when nothing is due.
	Poll time.Duration
	// Workers is how many webhooks are being sent at once, to all
	// endpoints together.
	Workers int
	// PerEndpoint is how many are being sent at once to one endpoint. Kept
	// under Workers, it is what stops an endpoint that hangs from holding
	// up the others.
	PerEndpoint int
	// Retries are the waits before each further try. A delivery is tried
	// once and then once after each wait; when they are used up, it is
	// given up.
	Retries []time.Duration
	// PauseAfter is how long an endpoint may fail every try before it is
	// paused; 0 never pauses.
	PauseAfter time.Duration
}

const (
	// settleTimeout bounds writing down what a try met.
	settleTimeout = 5 * time.Second
	// longestRest caps the rest between passes that fail.
	longestRest = 5 * time.Minute
)

// Worker delivers what the endpoints are owed.
type Worker struct {
	store  store.Webhooks
	poster *Poster
	opt    Options
	log    *slog.Logger
	// failing is set while what is owed cannot be read, for Healthy.
	failing atomic.Bool
}

// NewWorker returns a worker.
func NewWorker(s store.Webhooks, p *Poster, opt Options, log *slog.Logger) *Worker {
	return &Worker{store: s, poster: p, opt: opt, log: log}
}

// Healthy reports whether what is owed could last be read.
func (w *Worker) Healthy() bool { return !w.failing.Load() }

// lease is how long a delivery taken stays this worker's: a try starts as
// soon as it is taken, so one try and its settling, with room to spare.
func (w *Worker) lease() time.Duration { return w.poster.Timeout() + 2*settleTimeout }

// Run delivers until the context ends. Tries then in flight are cut short
// and handed back uncounted, so stopping takes moments, not a timeout; a
// webhook cut short may have arrived, and is then sent again later under
// the same id.
func (w *Worker) Run(ctx context.Context) { _, _ = w.work(ctx, false) }

// Pass delivers all that is due now, and what comes due meanwhile, and
// answers how many tries it made.
func (w *Worker) Pass(ctx context.Context) (int, error) { return w.work(ctx, true) }

// tally counts what tries came to, for the log.
type tally struct{ delivered, missed, gaveUp int }

// work is the worker's loop. Deliveries are taken as slots come free and
// tried at once, so the pace is each endpoint's own: a fast one is sent to
// as fast as it answers, whatever a slow one is doing. An endpoint is
// given no more while tries to it are in flight. once makes it return
// when nothing is due and nothing is in flight.
func (w *Worker) work(ctx context.Context, once bool) (int, error) {
	var (
		done     = make(chan result, w.opt.Workers)
		busy     = map[string]int{} // tries in flight, by endpoint
		inFlight int
		tries    int
		counts   tally
		rest     = w.opt.Poll
		stopping = ctx.Done()
	)
	for {
		took := 0
		if ctx.Err() == nil && inFlight < w.opt.Workers {
			batch, err := w.store.TakeDeliveries(ctx, w.opt.Workers-inFlight, w.opt.PerEndpoint, keys(busy), w.lease())
			switch {
			case err != nil && ctx.Err() != nil:
				// Stopping: nothing was taken, and nothing is wrong.
			case err != nil:
				w.failing.Store(true)
				w.log.Error("webhook: what the endpoints are owed could not be read; trying again", "error", err, "in", rest.String())
				if once {
					return tries, err
				}
			default:
				if w.failing.Swap(false) {
					w.log.Info("webhook: sending again")
				}
				rest = w.opt.Poll
			}
			for _, d := range batch {
				busy[d.Endpoint.ID]++
				go func() { done <- result{d.Endpoint.ID, w.try(ctx, d)} }()
			}
			took, inFlight, tries = len(batch), inFlight+len(batch), tries+len(batch)
		}
		if inFlight == 0 && took == 0 {
			if counts != (tally{}) {
				// Counts only: an endpoint's URL and an email's addresses stay out.
				w.log.Info("webhook: webhooks were sent", "delivered", counts.delivered,
					"to_try_again", counts.missed, "given_up", counts.gaveUp)
				counts = tally{}
			}
			if once || ctx.Err() != nil {
				return tries, nil
			}
		}
		var wake <-chan time.Time
		if ctx.Err() == nil {
			wake = time.After(rest)
		}
		select {
		case r := <-done:
			inFlight--
			if busy[r.endpoint]--; busy[r.endpoint] == 0 {
				delete(busy, r.endpoint)
			}
			counts.add(r.state)
		case <-wake:
			if w.failing.Load() {
				rest = min(rest*2, longestRest)
			}
		case <-stopping:
			stopping = nil // noted; the loop now only waits for what is in flight
		}
	}
}

// result is a try finished: whose it was and the state it left.
type result struct{ endpoint, state string }

func (t *tally) add(state string) {
	switch state {
	case store.DeliveryDelivered:
		t.delivered++
	case store.DeliveryFailed:
		t.gaveUp++
	case store.DeliveryPending:
		t.missed++
	}
}

func keys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
