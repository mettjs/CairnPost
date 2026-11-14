// Package prune deletes, on a schedule, what CairnPost has kept long
// enough: what emails said, where the operator keeps that at all,
// Idempotency-Keys past their life, emails' records past the time the
// operator keeps them, and lifted suppressions nothing can undo any more.
// A suppression in force is never pruned.
//
// Beside that, at a pace of its own, it settles what a server that
// stopped mid-send left undone: a send nobody is waiting on any more is
// recorded as unknown.
package prune

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

const (
	// batch is how many rows one delete takes: small enough that no
	// delete holds its rows for long.
	batch = 500
	// batchTimeout bounds one delete. A delete waits for rows another
	// session holds; without a bound, one that never lets go would stop
	// the pruner for good and nothing would say so.
	batchTimeout = time.Minute
	// firstRetry is the rest after a pass that failed, doubled each time
	// up to longestRetry or the rest between passes, whichever is less.
	firstRetry   = 5 * time.Second
	longestRetry = 5 * time.Minute
)

// Options are the pruner's settings.
type Options struct {
	// Every is how long the pruner rests between passes.
	Every time.Duration
	// KeyTTL is how long an Idempotency-Key is remembered.
	KeyTTL time.Duration
	// Keep is how long an email's record is kept; 0 keeps records for
	// ever, and then only keys and kept words are pruned.
	Keep time.Duration
	// Unanswered is how long a send may stay "sending" before it is
	// recorded as unknown, and how often that is looked for; 0 never
	// looks. It must be longer than any send is given.
	Unanswered time.Duration
}

// Pruner forgets what has been kept long enough.
type Pruner struct {
	store store.Pruning
	opt   Options
	log   *slog.Logger
	// failing is set while a pass could not finish, for Healthy.
	failing atomic.Bool
}

// New returns a pruner.
func New(s store.Pruning, opt Options, log *slog.Logger) *Pruner {
	return &Pruner{store: s, opt: opt, log: log}
}

// Healthy reports whether the last pass finished.
func (p *Pruner) Healthy() bool { return !p.failing.Load() }

// Run prunes now and then after every rest, until the context ends. A pass
// that fails is tried again soon, not a whole rest later: most failures
// pass by themselves, and what it left undone is still due.
func (p *Pruner) Run(ctx context.Context) {
	if p.opt.Unanswered > 0 {
		var sweeping sync.WaitGroup
		sweeping.Go(func() { p.sweeps(ctx) })
		defer sweeping.Wait()
	}
	retry := firstRetry
	for {
		c, err := p.Pass(ctx)
		rest := p.opt.Every
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			rest = min(retry, p.opt.Every)
			retry = min(retry*2, longestRetry)
			p.failing.Store(true)
			p.log.Error("prune: a pass could not finish; trying again", "error", err, "in", rest.String())
		default:
			retry = firstRetry
			if p.failing.Swap(false) {
				p.log.Info("prune: pruning again")
			}
		}
		if c != (Counts{}) {
			// Counts only: no address and no id.
			p.log.Info("prune: forgot what was kept long enough", "contents", c.Contents, "details", c.Details, "console_sessions", c.Sessions, "idempotency_keys", c.Keys,
				"emails", c.Emails, "lifted_suppressions", c.Lifted)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(rest):
		}
	}
}

// sweeps looks, until the context ends, for sends left unanswered. A send
// is found between one and two times Unanswered after it began.
func (p *Pruner) sweeps(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.opt.Unanswered):
		}
		switch n, err := p.Sweep(ctx); {
		case ctx.Err() != nil:
			return
		case err != nil:
			p.log.Error("prune: sends left unanswered could not be looked for; trying again", "error", err, "in", p.opt.Unanswered.String())
		case n > 0:
			// A count only. It is worth a warning: each may or may not
			// have gone, and nobody was told.
			p.log.Warn("prune: sends were left unanswered, by a server that stopped in the middle of them, "+
				"and are now recorded as unknown", "sends", n)
		}
	}
}

// Sweep records as unknown every send still "sending" after Unanswered,
// and answers how many.
func (p *Pruner) Sweep(ctx context.Context) (int, error) {
	return all(ctx, func(ctx context.Context) (int, error) { return p.store.MarkUnanswered(ctx, p.opt.Unanswered, batch) })
}

// Counts is how much a pass deleted.
type Counts struct{ Contents, Details, Sessions, Keys, Emails, Lifted int }

// Pass prunes everything due now. What emails said, and what was said of
// them, goes first and each on its own: it is the most private thing
// kept, so nothing else failing holds it back, and its failing holds
// nothing else back. Each was given its time when it was kept, so this
// runs whatever the setting is now.
func (p *Pruner) Pass(ctx context.Context) (Counts, error) {
	var (
		c    Counts
		errs [4]error
	)
	c.Contents, errs[0] = all(ctx, func(ctx context.Context) (int, error) { return p.store.PruneContents(ctx, batch) })
	c.Details, errs[1] = all(ctx, func(ctx context.Context) (int, error) { return p.store.PruneDetails(ctx, batch) })
	c.Sessions, errs[2] = all(ctx, func(ctx context.Context) (int, error) { return p.store.PruneSessions(ctx, batch) })
	errs[3] = p.records(ctx, &c)
	return c, errors.Join(errs[:]...)
}

// records prunes keys, then emails' records, then lifted suppressions.
// Keys go first: an email's record is not deleted while a key still points
// at it; lifted suppressions go last, once the emails sent before their
// lift are gone.
func (p *Pruner) records(ctx context.Context, c *Counts) error {
	var err error
	c.Keys, err = all(ctx, func(ctx context.Context) (int, error) { return p.store.PruneKeys(ctx, p.opt.KeyTTL, batch) })
	if err != nil || p.opt.Keep == 0 {
		return err
	}
	horizon, cancel := context.WithTimeout(ctx, batchTimeout)
	before, err := p.store.Horizon(horizon, p.opt.Keep)
	cancel()
	if err != nil {
		return err
	}
	c.Emails, err = all(ctx, func(ctx context.Context) (int, error) { return p.store.PruneMessages(ctx, before, batch) })
	if err != nil {
		return err
	}
	c.Lifted, err = all(ctx, func(ctx context.Context) (int, error) { return p.store.PruneLifted(ctx, before, batch) })
	return err
}

// all repeats a prune until it finds less than a batch, and answers the
// total. Each batch has its own deadline, and it stops between batches
// when the context ends.
func all(ctx context.Context, prune func(context.Context) (int, error)) (int, error) {
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		bounded, cancel := context.WithTimeout(ctx, batchTimeout)
		n, err := prune(bounded)
		cancel()
		total += n
		if err != nil || n < batch {
			return total, err
		}
	}
}
