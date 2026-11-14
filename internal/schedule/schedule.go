// Package schedule sends the emails that were asked for ahead of time,
// when their time comes. Each is taken from those that wait by one
// server alone, sent as any email is, and forgotten here: what became
// of it is then with the messages.
//
// An email the mail provider does not take is tried again, later each
// time, until GiveUp has passed since it was due. One whose answer was
// lost is not sent again: nobody is there to ask for a second one.
package schedule

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// Sender sends an email that waited; *mail.Service is one.
type Sender interface {
	SendHeld(ctx context.Context, h store.Held, again bool) error
}

// Options are the sender's settings.
type Options struct {
	// Poll is how often it looks for emails whose time has come.
	Poll time.Duration
	// Batch is how many it takes at one look, and Parallel how many of
	// them it sends at one time.
	Batch, Parallel int
	// GiveUp is how long after its time an email is still tried again
	// when the mail provider does not take it.
	GiveUp time.Duration
	// Abandoned is how long an email may stay taken before the server
	// that took it is held to have stopped: longer than any send.
	Abandoned time.Duration
}

const (
	// passTimeout bounds one look at the store.
	passTimeout = 30 * time.Second
	// firstWait is how long an email waits after its first failed try,
	// doubled each time up to longestWait.
	firstWait   = time.Minute
	longestWait = time.Hour
	// pause is how long the whole queue rests when the provider says its
	// limit is reached and does not say for how long.
	pause = time.Minute
)

// Runner sends what waits.
type Runner struct {
	store  store.Schedule
	sender Sender
	opt    Options
	now    func() time.Time
	log    *slog.Logger
	// failing is set while a look could not be made, for Healthy.
	failing atomic.Bool
}

// New returns a runner on the clock given.
func New(s store.Schedule, sender Sender, opt Options, now func() time.Time, log *slog.Logger) *Runner {
	return &Runner{store: s, sender: sender, opt: opt, now: now, log: log}
}

// Healthy reports whether the last look at what waits could be made.
func (r *Runner) Healthy() bool { return !r.failing.Load() }

// Run sends what is due, then looks again after every Poll, until the
// context ends. A send under way then is finished: it does not stop
// with the context.
func (r *Runner) Run(ctx context.Context) {
	looked := time.Time{}
	for {
		rest := r.opt.Poll
		if r.now().Sub(looked) >= r.opt.Abandoned {
			looked = r.now()
			r.recover(ctx)
		}
		n, wait, err := r.Pass(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			if !r.failing.Swap(true) {
				r.log.Error("schedule: the emails waiting to be sent could not be read; trying again", "error", err)
			}
		default:
			if r.failing.Swap(false) {
				r.log.Info("schedule: reading the emails waiting to be sent again")
			}
			switch {
			case wait > 0:
				rest = wait
			case n == r.opt.Batch:
				rest = 0 // more are due
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(rest):
		}
	}
}

// recover hands back what a stopped server had taken.
func (r *Runner) recover(ctx context.Context) {
	bounded, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()
	switch n, err := r.store.Recover(bounded, r.opt.Abandoned, r.opt.Batch); {
	case err != nil && ctx.Err() == nil:
		r.log.Error("schedule: emails a stopped server had taken could not be looked for", "error", err)
	case n > 0:
		r.log.Warn("schedule: emails were left taken by a server that stopped; those not yet handed to the mail provider wait again", "emails", n)
	}
}

// Pass takes the emails whose time has come and sends them. It answers
// how many it took, and how long the mail provider asked everything to
// wait, if it did.
func (r *Runner) Pass(ctx context.Context) (taken int, wait time.Duration, err error) {
	bounded, cancel := context.WithTimeout(ctx, passTimeout)
	due, err := r.store.TakeDue(bounded, r.opt.Batch)
	cancel()
	if err != nil {
		return 0, 0, err
	}
	var (
		mu      sync.Mutex
		sending sync.WaitGroup
		next    = make(chan store.Held)
	)
	for range min(max(r.opt.Parallel, 1), len(due)) {
		sending.Go(func() {
			for h := range next {
				mu.Lock()
				held := wait
				mu.Unlock()
				if held > 0 {
					// The limit was said meanwhile: not tried, not counted.
					r.settle(ctx, h, r.store.Postpone(context.WithoutCancel(ctx), h.ID, held))
					continue
				}
				if asked := r.send(ctx, h); asked > 0 {
					mu.Lock()
					wait = max(wait, asked)
					mu.Unlock()
				}
			}
		})
	}
	for _, h := range due {
		next <- h
	}
	close(next)
	sending.Wait()
	return len(due), wait, nil
}

// send sends one email and settles it among those that wait. It answers
// how long the mail provider asked everything to wait, if it did.
func (r *Runner) send(ctx context.Context, h store.Held) time.Duration {
	// What was taken is seen through, whoever stops meanwhile.
	free := context.WithoutCancel(ctx)
	last := r.now().Sub(h.SendAt) >= r.opt.GiveUp
	err := r.sender.SendHeld(free, h, !last)
	var throttled *driver.Throttled
	switch {
	case errors.As(err, &throttled) && !last:
		asked := throttled.After
		if asked <= 0 {
			asked = pause
		}
		r.settle(ctx, h, r.store.Postpone(free, h.ID, asked))
		return asked
	case errors.Is(err, driver.ErrUnavailable) && !last:
		wait := min(firstWait<<min(h.Tries-1, 10), longestWait)
		r.log.Warn("schedule: the mail provider did not take a waiting email; it will be tried again",
			"message", h.ID, "try", h.Tries, "in", wait.String())
		r.settle(ctx, h, r.store.Postpone(free, h.ID, wait))
	case errors.Is(err, mail.ErrSuppressed):
		r.settle(ctx, h, r.store.Drop(free, h.ID, store.HeldSuppressed))
	case errors.Is(err, mail.ErrGone):
		// Cancelled, or sent by another: nothing here to settle.
	case err == nil, errors.Is(err, driver.ErrRejected), errors.Is(err, driver.ErrUnavailable), errors.Is(err, mail.ErrInDoubt):
		// Sent, refused for good, given up on, or in doubt: its message
		// says which, and it waits no more.
		if last && errors.Is(err, driver.ErrUnavailable) {
			r.log.Warn("schedule: a waiting email was given up on; the mail provider did not take it in time", "message", h.ID, "tries", h.Tries)
		}
		r.settle(ctx, h, r.store.Release(free, h.ID))
	default:
		// A failure of our own before anything was sent: it waits again.
		r.log.Error("schedule: a waiting email could not be sent; it will be tried again", "message", h.ID, "error", err)
		r.settle(ctx, h, r.store.Postpone(free, h.ID, firstWait))
	}
	return 0
}

// settle says so when what became of a taken email could not be written
// down: it stays taken until it is recovered.
func (r *Runner) settle(_ context.Context, h store.Held, err error) {
	if err != nil && !errors.Is(err, store.ErrTooLate) && !errors.Is(err, store.ErrNotFound) {
		r.log.Error("schedule: what became of a waiting email could not be recorded", "message", h.ID, "error", err)
	}
}
