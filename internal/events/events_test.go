package events_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/events"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// queue is a source that hands over what it holds and remembers how each
// item was settled.
type queue struct {
	items       []driver.Received
	done, again []string
	settleErr   error
	takes       int
}

func (q *queue) Take(context.Context, int, time.Duration) ([]driver.Received, error) {
	q.takes++
	out := q.items
	q.items = nil
	return out, nil
}

func (q *queue) Settle(_ context.Context, done, again []string, _ time.Duration) error {
	q.done, q.again = append(q.done, done...), append(q.again, again...)
	return q.settleErr
}

// broken is a store that cannot record.
type broken struct{ store.Events }

func (broken) RecordEvent(context.Context, store.Event) (store.Recorded, error) {
	return 0, errors.New("the database is gone")
}

type rig struct {
	t      *testing.T
	store  *memstore.Store
	queue  *queue
	reader *events.Reader
	now    time.Time
	logged bytes.Buffer
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, store: memstore.New(), queue: &queue{}, now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	r.store.Now = func() time.Time { return r.now }
	r.reader = r.readerOn(r.store, r.store.Ping)
	return r
}

var options = events.Options{Poll: time.Second, Batch: 50, Lease: time.Minute, Retry: 30 * time.Second, Grace: 2 * time.Minute}

func (r *rig) readerOn(s store.Events, ping func(context.Context) error) *events.Reader {
	return events.New(s, ping, r.queue, "fake", options, func() time.Time { return r.now },
		slog.New(slog.NewTextHandler(&r.logged, nil)))
}

// sent records a message the upstream took under an id, and answers ours.
func (r *rig) sent(upstreamID string) string { return r.sentTo(upstreamID, "ana@example.org") }

func (r *rig) sentTo(upstreamID, recipient string) string {
	r.t.Helper()
	c, err := r.store.Start(context.Background(), store.Start{KeyID: "k", Sender: "app@example.com",
		Recipient: recipient, Driver: "fake"})
	if err != nil || c.Outcome != store.Started {
		r.t.Fatalf("the send did not start: %v %v", c.Outcome, err)
	}
	if err := r.store.Sent(context.Background(), c.ID, upstreamID); err != nil {
		r.t.Fatal(err)
	}
	return c.ID
}

func (r *rig) event(lease, id, upstreamID, kind string) driver.Received {
	return driver.Received{Lease: lease, Readable: true,
		Event: driver.Event{ID: id, UpstreamID: upstreamID, Kind: kind, At: r.now}}
}

func (r *rig) state(id string) string {
	r.t.Helper()
	m, err := r.store.Message(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return m.State
}

func TestAnEventIsRecordedThenAcknowledged(t *testing.T) {
	r := newRig(t)
	id := r.sent("up-1")
	bounce := r.event("l1", "e1", "up-1", driver.Bounced)
	bounce.Event.Permanent, bounce.Event.Code, bounce.Event.Reason = true, "5.1.1", "permanent_failure"
	r.queue.items = []driver.Received{bounce}
	if n, err := r.reader.Pass(context.Background()); n != 1 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	got, _ := r.store.EventsOf(context.Background(), id)
	if r.state(id) != store.StateBounced || len(got) != 1 || !got[0].Permanent || got[0].Code != "5.1.1" ||
		got[0].Reason != "permanent_failure" || strings.Join(r.queue.done, ",") != "l1" {
		t.Fatalf("state %s, events %+v, done %v", r.state(id), got, r.queue.done)
	}
	if strings.Contains(r.logged.String(), "ana@example.org") {
		t.Fatalf("the log holds an address: %s", r.logged.String())
	}
}

// Every kind a driver can report is one the store keeps and gives a state.
func TestEveryKindIsMappedThrough(t *testing.T) {
	for i, kind := range driver.Kinds {
		r := newRig(t)
		id := r.sent("up-1")
		r.queue.items = []driver.Received{r.event("l", string(rune('a'+i)), "up-1", kind)}
		if _, err := r.reader.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got, _ := r.store.EventsOf(context.Background(), id); len(got) != 1 || r.state(id) != store.StateOf[got[0].Kind] ||
			store.StateOf[got[0].Kind] == "" {
			t.Errorf("%s: events %+v, state %s", kind, got, r.state(id))
		}
	}
}

func TestTheSameEventTwiceIsOneEvent(t *testing.T) {
	r := newRig(t)
	id := r.sent("up-1")
	r.queue.items = []driver.Received{r.event("l1", "e1", "up-1", driver.Delivered), r.event("l2", "e1", "up-1", driver.Delivered)}
	r.reader.Pass(context.Background())
	if got, _ := r.store.EventsOf(context.Background(), id); len(got) != 1 || len(r.queue.done) != 2 {
		t.Fatalf("events %d, acknowledged %v", len(got), r.queue.done)
	}
}

// Events arrive in any order. A message only moves forward.
func TestAMessageOnlyMovesForward(t *testing.T) {
	r := newRig(t)
	id := r.sent("up-1")
	for i, step := range []struct{ kind, want string }{
		{driver.Deferred, store.StateDeferred},
		{driver.Delivered, store.StateDelivered},
		{driver.Deferred, store.StateDelivered},    // a late "not now" does not undo a delivery
		{driver.Complained, store.StateComplained}, // a complaint follows a delivery
		{driver.Delivered, store.StateComplained},
	} {
		r.queue.items = []driver.Received{r.event("l", string(rune('a'+i)), "up-1", step.kind)}
		r.reader.Pass(context.Background())
		if r.state(id) != step.want {
			t.Fatalf("after %s (step %d): %s, want %s", step.kind, i, r.state(id), step.want)
		}
	}
	// An email the upstream took and then would not send is no longer "sent".
	other := r.sentTo("up-2", "bo@example.org") // the first address complained, and is suppressed
	r.queue.items = []driver.Received{r.event("l", "f1", "up-2", driver.Failed)}
	r.reader.Pass(context.Background())
	if r.state(other) != store.StateFailed {
		t.Fatalf("a failed email is %s", r.state(other))
	}
}

// Only a bounce for good and a complaint suppress an address, and then for
// the domain that sent.
func TestOnlyAHardBounceOrAComplaintSuppresses(t *testing.T) {
	for name, c := range map[string]struct {
		kind      string
		permanent bool
		code      string
		reason    string
	}{
		"a hard bounce": {driver.Bounced, true, "5.1.1", store.ReasonBounce},
		"a complaint":   {driver.Complained, false, "", store.ReasonComplaint},
		"a soft bounce": {driver.Bounced, false, "4.2.2", ""},
		"a delivery":    {driver.Delivered, false, "", ""},
		"a deferral":    {driver.Deferred, false, "", ""},
		"a failure":     {driver.Failed, false, "", ""},
		// Refused for good, but over the sender's standing, not the
		// mailbox: a broken signature must not shut out everyone written to.
		"a policy refusal": {driver.Bounced, true, "5.7.26", ""},
	} {
		r := newRig(t)
		id := r.sent("up-1")
		e := r.event("l", "e", "up-1", c.kind)
		e.Event.Permanent, e.Event.Code = c.permanent, c.code
		r.queue.items = []driver.Received{e, e} // handed over twice: still one suppression
		r.reader.Pass(context.Background())
		list, _ := r.store.ListSuppressions(context.Background(), "", nil, 10)
		switch {
		case c.reason == "" && len(list) != 0:
			t.Errorf("%s suppressed %+v", name, list)
		case c.reason != "" && (len(list) != 1 || list[0].Reason != c.reason || list[0].Domain != "example.com" ||
			list[0].Address != "ana@example.org" || list[0].MessageID != id):
			t.Errorf("%s: %+v", name, list)
		}
	}
}

// Delivered, bounced and failed are outcomes of one rank: whichever happened
// later stands, whatever order the events came in.
func TestOfTwoOutcomesTheLaterStands(t *testing.T) {
	for _, order := range [][]int{{0, 1}, {1, 0}} {
		r := newRig(t)
		id := r.sent("up-1")
		delivered := r.event("l", "d", "up-1", driver.Delivered)
		bounced := r.event("l", "b", "up-1", driver.Bounced)
		bounced.Event.At = r.now.Add(time.Minute) // the bounce happened after the delivery
		pair := []driver.Received{delivered, bounced}
		for _, n := range order {
			r.queue.items = []driver.Received{pair[n]}
			r.reader.Pass(context.Background())
		}
		if r.state(id) != store.StateBounced {
			t.Errorf("arriving in order %v: %s", order, r.state(id))
		}
	}
}

// An event saying the email never went frees its Idempotency-Key, as a
// refusal at send does: the retry sends, instead of replaying a failure.
func TestAnEmailThatFailedAfterItWasTakenFreesItsKey(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	claim := store.Start{KeyID: "k", Sender: "app@example.com", Recipient: "ana@example.org", Driver: "fake",
		IdemKey: "once", Fingerprint: []byte("f"), TTL: time.Hour, InDoubt: time.Minute}
	first, _ := r.store.Start(ctx, claim)
	r.store.Sent(ctx, first.ID, "up-1")
	if again, _ := r.store.Start(ctx, claim); again.Outcome != store.Replay {
		t.Fatalf("before the event: %v", again.Outcome)
	}
	r.queue.items = []driver.Received{r.event("l", "f", "up-1", driver.Failed)}
	r.reader.Pass(ctx)
	if again, _ := r.store.Start(ctx, claim); again.Outcome != store.Started || r.state(first.ID) != store.StateFailed {
		t.Fatalf("after the event: %v, state %s", again.Outcome, r.state(first.ID))
	}
}

// An event that does not say when it happened is given the time it was read.
func TestAnEventWithoutATimeIsStampedWhenRead(t *testing.T) {
	r := newRig(t)
	id := r.sent("up-1")
	timeless := r.event("l", "e", "up-1", driver.Delivered)
	timeless.Event.At = time.Time{}
	r.queue.items = []driver.Received{timeless}
	r.reader.Pass(context.Background())
	if got, _ := r.store.EventsOf(context.Background(), id); len(got) != 1 || !got[0].At.Equal(r.now) {
		t.Fatalf("%+v", got)
	}
}

// An event can outrun the record of its send. It is handed back while it
// is young, and let go once it is plainly about an email that is not ours.
func TestAnEventAboutNoKnownEmailWaitsThenIsLetGo(t *testing.T) {
	r := newRig(t)
	r.queue.items = []driver.Received{r.event("young", "e1", "up-9", driver.Delivered)}
	r.reader.Pass(context.Background())
	if strings.Join(r.queue.again, ",") != "young" || len(r.queue.done) != 0 {
		t.Fatalf("a young stranger: again %v, done %v", r.queue.again, r.queue.done)
	}
	old := r.event("old", "e2", "up-9", driver.Delivered)
	old.Event.At = r.now.Add(-2 * time.Minute)
	r.queue.items = []driver.Received{old}
	r.reader.Pass(context.Background())
	if strings.Join(r.queue.done, ",") != "old" {
		t.Fatalf("an old stranger: done %v", r.queue.done)
	}
}

func TestWhatIsNoEventOfOursIsAcknowledgedAndSaid(t *testing.T) {
	r := newRig(t)
	unnamed := driver.Received{Lease: "unnamed", Event: driver.Event{Kind: driver.Bounced}}
	r.queue.items = []driver.Received{{Lease: "junk"}, unnamed}
	r.reader.Pass(context.Background())
	log := r.logged.String()
	if strings.Join(r.queue.done, ",") != "junk,unnamed" || !strings.Contains(log, "not_email_events=1") ||
		!strings.Contains(log, "do not name their email") {
		t.Fatalf("done %v, log %s", r.queue.done, log)
	}
}

// An upstream gives an event only so many times. While the store cannot be
// reached nothing is taken, so an outage spends none of them.
func TestNothingIsTakenWhileTheStoreIsDown(t *testing.T) {
	r := newRig(t)
	reader := r.readerOn(r.store, func(context.Context) error { return errors.New("no route") })
	r.queue.items = []driver.Received{r.event("l1", "e1", "up-1", driver.Delivered)}
	if _, err := reader.Pass(context.Background()); err == nil || r.queue.takes != 0 || len(r.queue.items) != 1 {
		t.Fatalf("err %v, takes %d", err, r.queue.takes)
	}
}

// An event that could not be recorded is handed back, not acknowledged and
// not left to lapse; the pass says it failed, and the reader says it is
// not healthy until a pass goes through.
func TestAnEventThatCouldNotBeRecordedIsHandedBack(t *testing.T) {
	r := newRig(t)
	reader := r.readerOn(broken{r.store}, r.store.Ping)
	last := r.event("l1", "e1", "up-1", driver.Delivered)
	last.Attempts = 2
	r.queue.items = []driver.Received{last, {Lease: "junk"}}
	if _, err := reader.Pass(context.Background()); err == nil {
		t.Fatal("a pass that recorded nothing reported no error")
	}
	if strings.Join(r.queue.done, ",") != "junk" || strings.Join(r.queue.again, ",") != "l1" ||
		!strings.Contains(r.logged.String(), "last delivery") {
		t.Fatalf("done %v, again %v, log %s", r.queue.done, r.queue.again, r.logged.String())
	}
}

// Run stops when told to, and a pass in flight is not cut short.
func TestRunStopsAndFinishesItsPass(t *testing.T) {
	r := newRig(t)
	id := r.sent("up-1")
	r.queue.items = []driver.Received{r.event("l1", "e1", "up-1", driver.Delivered)}
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { r.reader.Run(ctx); close(stopped) }()
	for i := 0; r.state(id) != store.StateDelivered; i++ {
		if i > 2000 {
			t.Fatal("the event was never recorded")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if !r.reader.Healthy() || strings.Join(r.queue.done, ",") != "l1" {
		t.Fatalf("healthy %v, done %v", r.reader.Healthy(), r.queue.done)
	}
}

func (r *rig) suppressed() []store.Suppression {
	r.t.Helper()
	list, err := r.store.ListSuppressions(context.Background(), "", nil, 10)
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

func (r *rig) hardBounce(eventID, upstreamID string) driver.Received {
	e := r.event("l", eventID, upstreamID, driver.Bounced)
	e.Event.Permanent, e.Event.Code = true, "5.1.1"
	return e
}

// A suppression lifted by hand stays lifted when a late event arrives about
// an email sent before the lift; an email sent after it suppresses again.
func TestALiftIsNotUndoneByAnOlderEmailsEvent(t *testing.T) {
	r := newRig(t)
	r.sent("up-1")
	r.sent("up-2") // both sent before anything bounced
	r.queue.items = []driver.Received{r.hardBounce("e1", "up-1")}
	r.reader.Pass(context.Background())
	r.now = r.now.Add(time.Minute)
	if err := r.store.Unsuppress(context.Background(), "example.com", "ana@example.org"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	r.queue.items = []driver.Received{r.hardBounce("e2", "up-2")}
	r.reader.Pass(context.Background())
	if list := r.suppressed(); len(list) != 0 {
		t.Fatalf("an email sent before the lift suppressed again: %+v", list)
	}
	later := r.sent("up-3")
	r.now = r.now.Add(time.Minute)
	r.queue.items = []driver.Received{r.hardBounce("e3", "up-3")}
	r.reader.Pass(context.Background())
	if list := r.suppressed(); len(list) != 1 || list[0].MessageID != later {
		t.Fatalf("an email sent after the lift: %+v", list)
	}
}

// A bounce for good about an email with no record here still suppresses,
// when the event names the addresses itself.
func TestABounceOfAnEmailWithNoRecordStillSuppresses(t *testing.T) {
	r := newRig(t)
	named := r.hardBounce("e1", "up-unknown")
	named.Event.Sender, named.Event.Recipient = "app@Example.com", "Gone@example.org"
	nameless := r.hardBounce("e2", "up-unknown-2")
	r.queue.items = []driver.Received{named, nameless}
	r.reader.Pass(context.Background())
	list := r.suppressed()
	if len(list) != 1 || list[0].Domain != "example.com" || list[0].Address != "gone@example.org" ||
		list[0].Reason != store.ReasonBounce || list[0].MessageID != "" {
		t.Fatalf("%+v", list)
	}
	if strings.Contains(r.logged.String(), "gone@") || strings.Contains(r.logged.String(), "Gone@") {
		t.Fatalf("the log names the address: %s", r.logged.String())
	} // When that email was sent is not known, so it never undoes a lift;
	// and when a recorded email then bounces, the suppression names it.
	r.now = r.now.Add(time.Minute)
	if err := r.store.Unsuppress(context.Background(), "example.com", "gone@example.org"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	again := r.hardBounce("e3", "up-unknown-3")
	again.Event.Sender, again.Event.Recipient = "app@example.com", "gone@example.org"
	r.queue.items = []driver.Received{again}
	r.reader.Pass(context.Background())
	if list := r.suppressed(); len(list) != 0 {
		t.Fatalf("an email with no record undid a lift: %+v", list)
	}
	first := r.hardBounce("e4", "up-unknown-4")
	first.Event.Sender, first.Event.Recipient = "app@example.com", "new@example.org"
	id := r.sentTo("up-5", "new@example.org")
	r.queue.items = []driver.Received{first, r.hardBounce("e5", "up-5")}
	r.reader.Pass(context.Background())
	if list := r.suppressed(); len(list) != 1 || list[0].MessageID != id {
		t.Fatalf("a suppression with no message was not given the one that bounced next: %+v", list)
	}
}

// A receiving server's sentence is kept only by a reader told how long to
// keep it, whatever the source hands over; and it is never logged.
func TestAServersSentenceIsKeptOnlyWhenContentIs(t *testing.T) {
	for keep, want := range map[time.Duration]int{0: 0, time.Hour: 1} {
		r := newRig(t)
		id := r.sent("up-1")
		bounce := r.event("l1", "e1", "up-1", driver.Bounced)
		bounce.Event.Detail = "550 SENTENCEMARK no such user"
		r.queue.items = []driver.Received{bounce}
		opt := options
		opt.KeepDetails = keep
		reader := events.New(r.store, func(context.Context) error { return nil }, r.queue, "fake", opt,
			func() time.Time { return r.now }, slog.New(slog.NewTextHandler(&r.logged, nil)))
		if n, err := reader.Pass(context.Background()); n != 1 || err != nil {
			t.Fatalf("%d %v", n, err)
		}
		said, err := r.store.Details(context.Background(), id, 1000*time.Hour)
		if err != nil || len(said) != want {
			t.Errorf("keeping for %s: %d sentences kept, want %d (%v)", keep, len(said), want, err)
		}
		if strings.Contains(r.logged.String(), "SENTENCEMARK") {
			t.Errorf("the log holds the sentence: %s", r.logged.String())
		}
	}
}
