package mail

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/store"
)

// ErrNotStarted: a batch ended before this email's turn, because it ran
// out of time, the server is stopping or the caller left. Nothing of it
// was sent or recorded; sending it again is right.
var ErrNotStarted = errors.New("mail: the batch ended before this email's turn; nothing was sent")

// BatchInvalid is a batch that is refused whole, with nothing sent: one
// of its emails can never be sent as it is, or may not be sent by this
// key. Index is that email's place, from 0; -1 when the fault is the
// batch's own. Err is an *Invalid or ErrDomain.
type BatchInvalid struct {
	Index int
	Err   error
}

func (e *BatchInvalid) Error() string { return e.Err.Error() }

// Unwrap is what was wrong.
func (e *BatchInvalid) Unwrap() error { return e.Err }

// Outcome is what became of one email of a batch: Send's answer for it.
type Outcome struct {
	Receipt
	Err error
}

// batchKeyRoom is what a batch's Idempotency-Key leaves for the place of
// each email, which is written after it.
const batchKeyRoom = 8

// batchKey is the Idempotency-Key of the email at place i of a batch
// sent under key: the same batch sent again finds each of its emails.
func batchKey(key string, i int) string {
	if key == "" {
		return ""
	}
	return fmt.Sprintf("%s#%d", key, i+1)
}

// SendBatch sends several emails and answers what became of each, in
// their order. Every email is checked before any is sent: one that could
// never be sent refuses the whole batch with a *BatchInvalid. After
// that each stands alone, as if sent by Send; one failing does not stop
// the others, save that once the upstream says its limit is reached the
// rest are not tried and answer the same.
//
// idem is the batch's Idempotency-Key, "" for none. Each email is kept
// under it and its place, so the same batch sent again sends only what
// did not go the first time.
func (s *Service) SendBatch(ctx context.Context, key store.Key, presented, idem string, reqs []Request) ([]Outcome, error) {
	switch {
	case len(reqs) == 0:
		return nil, &BatchInvalid{-1, &Invalid{"emails", "A batch must hold at least one email."}}
	case len(reqs) > s.opt.MaxBatch:
		return nil, &BatchInvalid{-1, &Invalid{"emails",
			fmt.Sprintf("A batch holds at most %d emails; this one holds %d. Send it in parts.", s.opt.MaxBatch, len(reqs))}}
	case len(idem) > maxIdemKey-batchKeyRoom || !idemKey.MatchString(idem):
		return nil, &BatchInvalid{-1, &Invalid{"Idempotency-Key",
			fmt.Sprintf("A batch's Idempotency-Key header must be at most %d printable ASCII characters.", maxIdemKey-batchKeyRoom)}}
	}
	ready := make([]checked, len(reqs))
	for i, r := range reqs {
		c, err := s.checked(key, r)
		if err != nil {
			return nil, &BatchInvalid{i, err}
		}
		ready[i] = c
	}
	run := &batchRun{ctx: ctx, stopping: s.opt.Stopping, until: time.Now().Add(s.opt.BatchTime)}
	out := make([]Outcome, len(reqs))
	var wg sync.WaitGroup
	for range min(s.opt.BatchParallel, len(reqs)) {
		wg.Go(func() {
			for {
				i, halted := run.next(len(reqs))
				if i < 0 {
					return
				}
				if halted != nil {
					out[i].Err = halted
					continue
				}
				out[i].Receipt, out[i].Err = s.send(ctx, key, presented, batchKey(idem, i), ready[i])
				var throttled *driver.Throttled
				if errors.As(out[i].Err, &throttled) {
					run.halt(out[i].Err)
				}
			}
		})
	}
	wg.Wait()
	return out, nil
}

// batchRun hands a batch's emails out by their place, and says when no
// further one is to be started.
type batchRun struct {
	ctx      context.Context
	stopping <-chan struct{}
	until    time.Time

	mu     sync.Mutex
	at     int
	halted error
}

// next is the place of the next email, -1 when there is none; with it,
// why it is not to be sent, if it is not.
func (b *batchRun) next(n int) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.at >= n {
		return -1, nil
	}
	if b.halted == nil {
		select {
		case <-b.ctx.Done():
			b.halted = ErrNotStarted
		case <-b.stopping:
			b.halted = ErrNotStarted
		default:
			if !time.Now().Before(b.until) {
				b.halted = ErrNotStarted
			}
		}
	}
	b.at++
	return b.at - 1, b.halted
}

// halt ends the batch: every email not yet started answers why.
func (b *batchRun) halt(why error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.halted == nil {
		b.halted = why
	}
}
