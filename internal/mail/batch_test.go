package mail_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// byAddress is a driver that answers by whom an email goes to, and may
// be asked by several at once.
type byAddress struct {
	mu     sync.Mutex
	refuse map[string]error
	sent   []string
	most   int // how many sends were under way at one time
	now    int
}

func (*byAddress) Name() string { return "fake" }

func (d *byAddress) Send(_ context.Context, m driver.Message) (string, error) {
	d.mu.Lock()
	d.now++
	d.most = max(d.most, d.now)
	d.mu.Unlock()
	time.Sleep(time.Millisecond)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.now--
	d.sent = append(d.sent, m.To[0])
	return "up-" + m.To[0], d.refuse[m.To[0]]
}

func batchRig(opt mail.Options) (*mail.Service, *byAddress, *memstore.Store) {
	s, d := memstore.New(), &byAddress{}
	opt.SendTimeout, opt.IdempotencyTTL, opt.InDoubtWindow = time.Second, time.Hour, time.Minute
	return mail.New(s, d, opt, slog.New(slog.NewTextHandler(io.Discard, nil))), d, s
}

func to(names ...string) []mail.Request {
	var out []mail.Request
	for _, n := range names {
		out = append(out, mail.Request{From: "app@example.com", To: []string{n + "@example.org"}, Subject: "Hello", Text: "Body"})
	}
	return out
}

var sender = store.Key{ID: "key-1", Scope: store.ScopeSend}

func TestABatchIsCheckedWholeBeforeAnyOfItIsSent(t *testing.T) {
	svc, drv, _ := batchRig(mail.Options{MaxBatch: 3})
	bad := to("ana", "ben")
	bad[1].Subject = ""
	bound := store.Key{ID: "key-2", Scope: store.ScopeSend, Domain: "other.example"}
	for name, c := range map[string]struct {
		key   store.Key
		idem  string
		reqs  []mail.Request
		index int
		is    error
	}{
		"an email that can never go": {sender, "", bad, 1, nil},
		"another domain's email":     {bound, "", to("ana"), 0, mail.ErrDomain},
		"no email":                   {sender, "", nil, -1, nil},
		"too many":                   {sender, "", to("a", "b", "c", "d"), -1, nil},
		"a key too long to number":   {sender, strings.Repeat("k", 250), to("ana"), -1, nil},
	} {
		out, err := svc.SendBatch(context.Background(), c.key, "cp_x", c.idem, c.reqs)
		var whole *mail.BatchInvalid
		var invalid *mail.Invalid
		if !errors.As(err, &whole) || whole.Index != c.index || out != nil || len(drv.sent) != 0 {
			t.Errorf("%s: %v, %v, %d sent", name, out, err, len(drv.sent))
		} else if c.is != nil && !errors.Is(err, c.is) || c.is == nil && !errors.As(err, &invalid) {
			t.Errorf("%s: the cause is %v", name, whole.Err)
		}
	}
}

func TestEachEmailOfABatchStandsAlone(t *testing.T) {
	svc, drv, s := batchRig(mail.Options{BatchParallel: 3})
	drv.refuse = map[string]error{"ben@example.org": driver.ErrRejected, "dee@example.org": errors.New("the line dropped")}
	out, err := svc.SendBatch(context.Background(), sender, "cp_x", "", to("ana", "ben", "cy", "dee", "eve", "fay"))
	if err != nil || len(out) != 6 || len(drv.sent) != 6 || drv.most < 2 || drv.most > 3 {
		t.Fatalf("the batch: %v, %d sent, %d at one time", err, len(drv.sent), drv.most)
	}
	for i, want := range []struct {
		err   error
		state string
	}{{nil, store.StateSent}, {driver.ErrRejected, store.StateFailed}, {nil, store.StateSent},
		{mail.ErrInDoubt, store.StateUnknown}, {nil, store.StateSent}, {nil, store.StateSent}} {
		if !errors.Is(out[i].Err, want.err) || out[i].ID == "" {
			t.Errorf("email %d: id %q, %v", i+1, out[i].ID, out[i].Err)
			continue
		}
		if m, err := s.Message(context.Background(), out[i].ID); err != nil || m.State != want.state {
			t.Errorf("email %d is %q on record: %v", i+1, m.State, err)
		}
	}
}

func TestOnceTheProviderSaysItsLimitTheRestAreNotTried(t *testing.T) {
	svc, drv, _ := batchRig(mail.Options{BatchParallel: 1})
	drv.refuse = map[string]error{"ben@example.org": &driver.Throttled{After: time.Minute}}
	out, _ := svc.SendBatch(context.Background(), sender, "cp_x", "", to("ana", "ben", "cy", "dee"))
	var throttled *driver.Throttled
	if len(drv.sent) != 2 || out[0].Err != nil || !errors.As(out[2].Err, &throttled) || !errors.As(out[3].Err, &throttled) || out[3].ID != "" {
		t.Fatalf("%d tried: %+v", len(drv.sent), out)
	}
}

func TestABatchStartsNothingOnceItIsOver(t *testing.T) {
	stopped := make(chan struct{})
	close(stopped)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		ctx context.Context
		opt mail.Options
	}{
		"the server is stopping": {context.Background(), mail.Options{Stopping: stopped}},
		"its time ran out":       {context.Background(), mail.Options{BatchTime: time.Nanosecond}},
		"the caller left":        {gone, mail.Options{}},
	} {
		svc, drv, s := batchRig(c.opt)
		out, err := svc.SendBatch(c.ctx, sender, "cp_x", "again", to("ana", "ben"))
		if err != nil || len(drv.sent) != 0 || !errors.Is(out[0].Err, mail.ErrNotStarted) || !errors.Is(out[1].Err, mail.ErrNotStarted) {
			t.Errorf("%s: %+v %v, %d sent", name, out, err, len(drv.sent))
		}
		// Nothing was claimed: the same batch goes when it is sent again.
		if kept, err := s.Messages(context.Background(), store.MessageQuery{Limit: 10}); err != nil || len(kept) != 0 {
			t.Errorf("%s: %d emails on record: %v", name, len(kept), err)
		}
	}
}
