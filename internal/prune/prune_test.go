package prune

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// rig is a store on a clock the test moves.
type rig struct {
	t   *testing.T
	s   *memstore.Store
	now time.Time
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, s: memstore.New(), now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	r.s.Now = func() time.Time { return r.now }
	return r
}

func (r *rig) days(n int) { r.now = r.now.Add(time.Duration(n) * 24 * time.Hour) }

// send records an email the upstream took under an id.
func (r *rig) send(up, to, idem string) string {
	r.t.Helper()
	c, err := r.s.Start(context.Background(), store.Start{KeyID: "k", Sender: "app@example.com", Recipient: to,
		Driver: "test", IdemKey: idem, Fingerprint: []byte("f"), TTL: 24 * time.Hour, InDoubt: time.Minute})
	if err != nil || c.Outcome != store.Started {
		r.t.Fatalf("%s did not start: %v %v", up, c.Outcome, err)
	}
	if up != "" {
		if err := r.s.Sent(context.Background(), c.ID, up); err != nil {
			r.t.Fatal(err)
		}
	}
	return c.ID
}

func (r *rig) bounce(up, to string) {
	r.t.Helper()
	if _, err := r.s.RecordEvent(context.Background(), store.Event{Driver: "test", UpstreamEventID: "evt-" + up + r.now.String(),
		UpstreamID: up, Kind: store.EventBounced, Permanent: true, Code: "5.1.1", At: r.now,
		Sender: "app@example.com", Recipient: to}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) suppressed() int {
	r.t.Helper()
	list, err := r.s.ListSuppressions(context.Background(), "example.com", nil, 10)
	if err != nil {
		r.t.Fatal(err)
	}
	return len(list)
}

func (r *rig) pass(keep time.Duration) Counts {
	r.t.Helper()
	c, err := New(r.s, Options{Every: time.Hour, KeyTTL: 24 * time.Hour, Keep: keep}, quiet).Pass(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

const month = 30 * 24 * time.Hour

func TestAPassForgetsKeysThenEmailsThenLifts(t *testing.T) {
	ctx, r := context.Background(), newRig(t)
	gone := r.send("<a>", "dead@example.org", "a")
	r.send("<b>", "back@example.org", "b")
	r.send("<c>", "back@example.org", "")
	doubt := r.send("", "other@example.org", "doubt") // its answer never came
	r.bounce("<a>", "dead@example.org")
	r.bounce("<b>", "back@example.org")
	if err := r.s.Unsuppress(ctx, "example.com", "back@example.org"); err != nil {
		t.Fatal(err)
	}
	if c := r.pass(month); c != (Counts{}) {
		t.Fatalf("a pass with nothing old forgot %+v", c)
	}

	r.days(2)
	if c := r.pass(month); c != (Counts{Keys: 3}) {
		t.Fatalf("after two days: %+v", c)
	}
	if m, _ := r.s.Message(ctx, doubt); m.State != store.StateUnknown {
		t.Fatalf("the try with no answer is %q, want unknown", m.State)
	}

	// A month on, the emails nothing has happened to since go. The one
	// found unknown two days in is kept two days more, and while it is,
	// so is the lift: an email sent before it is still on record.
	r.days(29)
	fresh := r.send("<d>", "new@example.org", "")
	if c := r.pass(month); c != (Counts{Emails: 3}) {
		t.Fatalf("after a month: %+v", c)
	}
	r.days(2)
	if c := r.pass(month); c != (Counts{Emails: 1, Lifted: 1}) {
		t.Fatalf("two days later: %+v", c)
	}
	if _, err := r.s.Message(ctx, gone); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a pruned email is still on record: %v", err)
	}
	if _, err := r.s.Message(ctx, fresh); err != nil {
		t.Fatalf("a new email went with the old: %v", err)
	}
	if list, _ := r.s.ListSuppressions(ctx, "example.com", nil, 10); len(list) != 1 || list[0].MessageID != "" {
		t.Fatalf("the suppression in force: %+v", list)
	}
}

func TestALiftOutlivesEveryEmailSentBeforeIt(t *testing.T) {
	ctx, r := context.Background(), newRig(t)
	r.send("<a>", "back@example.org", "")
	r.bounce("<a>", "back@example.org")
	r.days(20)
	r.send("<b>", "back2@example.org", "") // sent before the lift, bounces late
	if err := r.s.Unsuppress(ctx, "example.com", "back@example.org"); err != nil {
		t.Fatal(err)
	}
	r.days(15) // the first email is past a month, the lift is not
	if c := r.pass(month); c != (Counts{Emails: 1}) {
		t.Fatalf("%+v", c)
	}
	r.bounce("<a>", "back@example.org") // late, and its record is gone
	if n := r.suppressed(); n != 0 {
		t.Fatalf("a late bounce of a pruned email undid a lift: %d in force", n)
	}
	// The email sent before the lift has an event of its own, and so is
	// kept on. While it is, so is the lift, however old.
	r.days(20)
	if _, err := r.s.RecordEvent(ctx, store.Event{Driver: "test", UpstreamEventID: "late", UpstreamID: "<b>",
		Kind: store.EventDeferred, At: r.now}); err != nil {
		t.Fatal(err)
	}
	r.days(25)
	r.send("<c>", "new@example.org", "") // sent after the lift: holds nothing back
	if c := r.pass(month); c != (Counts{}) {
		t.Fatalf("with an email sent before the lift on record: %+v", c)
	}
	r.days(6) // that email is now a month quiet
	if c := r.pass(month); c != (Counts{Emails: 1, Lifted: 1}) {
		t.Fatalf("once it is gone: %+v", c)
	}
}

// An event is something that happened to an email: its record is kept
// from then, so a late bounce and its webhook are not cut short.
func TestAnEmailIsKeptFromTheLastThingThatHappenedToIt(t *testing.T) {
	ctx, r := context.Background(), newRig(t)
	id := r.send("<a>", "ana@example.org", "")
	r.days(25)
	r.bounce("<a>", "ana@example.org")
	r.days(10)
	if c := r.pass(month); c != (Counts{}) {
		t.Fatalf("ten days after its bounce: %+v", c)
	}
	if _, err := r.s.Message(ctx, id); err != nil {
		t.Fatal(err)
	}
	r.days(21)
	if c := r.pass(month); c != (Counts{Emails: 1}) {
		t.Fatalf("a month after its bounce: %+v", c)
	}
}

func TestKeepingForEverPrunesOnlyKeys(t *testing.T) {
	r := newRig(t)
	r.send("<a>", "ana@example.org", "a")
	r.days(400)
	if c := r.pass(0); c != (Counts{Keys: 1}) {
		t.Fatalf("%+v", c)
	}
}

// failing is a store whose prunes fail until told otherwise.
type failing struct {
	store.Pruning
	err error
}

func (f *failing) PruneKeys(ctx context.Context, ttl time.Duration, limit int) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.Pruning.PruneKeys(ctx, ttl, limit)
}

func TestAFailedPassIsSaidAndStoppingIsPrompt(t *testing.T) {
	f := &failing{Pruning: memstore.New(), err: errors.New("the database is away")}
	p := New(f, Options{Every: time.Hour, KeyTTL: time.Hour, Keep: month}, quiet)
	ctx, stop := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { defer close(ran); p.Run(ctx) }()
	for deadline := time.Now().Add(5 * time.Second); p.Healthy(); {
		if time.Now().After(deadline) {
			t.Fatal("a failed pass left the pruner healthy")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the pruner did not stop with its context")
	}
}

func TestMoreThanABatchIsPrunedInOnePass(t *testing.T) {
	r := newRig(t)
	for i := range batch + 10 {
		r.send("<"+time.Duration(i).String()+">", "ana@example.org", "key-"+time.Duration(i).String())
	}
	r.days(40)
	if c := r.pass(month); c.Keys != batch+10 || c.Emails != batch+10 {
		t.Fatalf("%+v", c)
	}
}
