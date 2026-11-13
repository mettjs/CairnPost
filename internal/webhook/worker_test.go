package webhook_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// receiver is an application's endpoint: it answers what it is told to and
// remembers what it was sent.
type receiver struct {
	mu     sync.Mutex
	status int
	got    []string // each webhook's id
	bodies []string
	srv    *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{status: http.StatusOK}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got, r.bodies = append(r.got, req.Header.Get("webhook-id")), append(r.bodies, string(body))
		w.WriteHeader(r.status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) answer(status int) { r.mu.Lock(); r.status = status; r.mu.Unlock() }

type rig struct {
	t      *testing.T
	store  *memstore.Store
	svc    *webhook.Service
	worker *webhook.Worker
	now    time.Time
	logged bytes.Buffer
}

var retries = []time.Duration{time.Minute, time.Hour}

func newRig(t *testing.T, insecure bool) *rig {
	r := &rig{t: t, store: memstore.New(), now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	r.store.Now = func() time.Time { return r.now }
	clock := func() time.Time { return r.now }
	poster := webhook.NewPoster(time.Second, true, 2, "test", clock)
	r.svc = webhook.NewService(r.store, poster, insecure, 3, clock)
	r.worker = webhook.NewWorker(r.store, poster, webhook.Options{Poll: 10 * time.Millisecond, Workers: 4,
		PerEndpoint: 2, Retries: retries}, slog.New(slog.NewTextHandler(&r.logged, nil)))
	return r
}

// bounce records a hard bounce of a new email from a domain.
func (r *rig) bounce(domain, up string) { r.t.Helper(); r.bounceFrom(domain, up, up+"@example.org") }

// bounceOf records a hard bounce of a new email to an address.
func (r *rig) bounceOf(up, to string) { r.t.Helper(); r.bounceFrom("example.com", up, to) }

func (r *rig) bounceFrom(domain, up, to string) {
	r.t.Helper()
	ctx := context.Background()
	c, err := r.store.Start(ctx, store.Start{KeyID: "k", Sender: "app@" + domain, Recipient: to, Driver: "fake"})
	if err != nil || c.Outcome != store.Started {
		r.t.Fatalf("%v %v", c.Outcome, err)
	}
	r.store.Sent(ctx, c.ID, up)
	if got, err := r.store.RecordEvent(ctx, store.Event{Driver: "fake", UpstreamEventID: "e-" + up, UpstreamID: up,
		Kind: store.EventBounced, Permanent: true, Code: "5.1.1", At: r.now}); err != nil || got != store.Kept {
		r.t.Fatalf("%v %v", got, err)
	}
}

func (r *rig) pass() int {
	r.t.Helper()
	n, err := r.worker.Pass(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) deliveries(id string) []store.Delivery {
	r.t.Helper()
	list, _, err := r.svc.Deliveries(context.Background(), "", id, "", "", 0)
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

func TestAnEventReachesItsEndpointSignedAndOnce(t *testing.T) {
	r, app := newRig(t, true), newReceiver(t)
	e, err := r.svc.Create(context.Background(), "", webhook.New{URL: app.srv.URL, Domain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r.bounce("example.com", "up-1")
	r.bounce("other.example", "up-2") // not this endpoint's domain
	if n := r.pass(); n != 1 || len(app.got) != 1 {
		t.Fatalf("%d taken, %d received", n, len(app.got))
	}
	if !strings.Contains(app.bodies[0], `"type":"email.bounced"`) || !strings.Contains(app.bodies[0], `"suppressed":true`) {
		t.Fatalf("the webhook: %s", app.bodies[0])
	}
	list := r.deliveries(e.ID)
	if len(list) != 1 || list[0].State != store.DeliveryDelivered || list[0].ID != app.got[0] || list[0].LastStatus != 200 {
		t.Fatalf("%+v", list)
	}
	if n := r.pass(); n != 0 || len(app.got) != 1 {
		t.Fatalf("a delivered webhook was sent again: %d", len(app.got))
	}
	// The log counts; it names no URL and no address.
	if log := r.logged.String(); strings.Contains(log, app.srv.URL) || strings.Contains(log, "@example") {
		t.Fatalf("the log says too much: %s", log)
	}
}

func TestAnEndpointThatDoesNotTakeItIsTriedAgainThenGivenUp(t *testing.T) {
	r, app := newRig(t, true), newReceiver(t)
	e, _ := r.svc.Create(context.Background(), "", webhook.New{URL: app.srv.URL})
	app.answer(http.StatusServiceUnavailable)
	r.bounce("example.com", "up-1")
	r.pass()
	if n := r.pass(); n != 0 {
		t.Fatal("tried again before its wait was over")
	}
	if d := r.deliveries(e.ID)[0]; d.State != store.DeliveryPending || d.Attempts != 1 || d.LastStatus != 503 ||
		d.LastFailure != webhook.FailStatus || !d.NextAt.Equal(r.now.Add(retries[0])) {
		t.Fatalf("after the first try: %+v", d)
	}
	r.now = r.now.Add(retries[0])
	r.pass()
	r.now = r.now.Add(retries[1])
	r.pass() // the last try: the waits are used up
	d := r.deliveries(e.ID)[0]
	if d.State != store.DeliveryFailed || d.Attempts != 3 || len(app.got) != 3 || !strings.Contains(r.logged.String(), "given up") {
		t.Fatalf("after every try: %+v, %d received", d, len(app.got))
	}
	// The same id on every try, so a receiver can tell a repeat.
	if app.got[0] != app.got[1] || app.got[1] != app.got[2] {
		t.Fatalf("ids changed between tries: %v", app.got)
	}
	r.now = r.now.Add(24 * time.Hour)
	if n := r.pass(); n != 0 {
		t.Fatal("a webhook given up was tried again")
	}
	// By hand it is tried once more, and only once, while the endpoint is
	// still down.
	if err := r.svc.Retry(context.Background(), "", e.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	r.pass()
	r.now = r.now.Add(24 * time.Hour)
	if n := r.pass(); n != 0 || len(app.got) != 4 || r.deliveries(e.ID)[0].State != store.DeliveryFailed {
		t.Fatalf("a retry by hand is one more try: %d received, %+v", len(app.got), r.deliveries(e.ID)[0])
	}
	// Once more, and now the endpoint takes it.
	app.answer(http.StatusNoContent)
	r.svc.Retry(context.Background(), "", e.ID, d.ID)
	r.pass()
	if d := r.deliveries(e.ID)[0]; d.State != store.DeliveryDelivered || d.LastFailure != "" {
		t.Fatalf("after a retry by hand: %+v", d)
	}
	if err := r.svc.Retry(context.Background(), "", e.ID, d.ID); err != store.ErrNotFound {
		t.Fatalf("retrying what was delivered: %v", err)
	}
}

func TestADisabledEndpointIsOwedNothingNewAndWhatItWasOwedWaits(t *testing.T) {
	r, app := newRig(t, true), newReceiver(t)
	ctx := context.Background()
	e, _ := r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL, Types: []string{"email.bounced"}})
	r.bounce("example.com", "up-1")
	off, on := false, true
	if _, err := r.svc.Update(ctx, "", e.ID, webhook.Change{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	r.bounce("example.com", "up-2") // while disabled: not owed
	if n := r.pass(); n != 0 || len(app.got) != 0 {
		t.Fatalf("sent to a disabled endpoint: %d", len(app.got))
	}
	r.svc.Update(ctx, "", e.ID, webhook.Change{Enabled: &on})
	if n := r.pass(); n != 1 || len(app.got) != 1 {
		t.Fatalf("once enabled: %d taken, %d received", n, len(app.got))
	}
}

// A webhook waiting for its next try can be sent now, without the wait.
func TestAWaitingWebhookCanBeSentNow(t *testing.T) {
	r, app := newRig(t, true), newReceiver(t)
	e, _ := r.svc.Create(context.Background(), "", webhook.New{URL: app.srv.URL})
	app.answer(http.StatusBadGateway)
	r.bounce("example.com", "up-1")
	r.pass()
	app.answer(http.StatusOK)
	if n := r.pass(); n != 0 {
		t.Fatal("tried before its wait was over")
	}
	if err := r.svc.Retry(context.Background(), "", e.ID, r.deliveries(e.ID)[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := r.pass(); n != 1 || r.deliveries(e.ID)[0].State != store.DeliveryDelivered {
		t.Fatalf("sent now: %d tries, %+v", n, r.deliveries(e.ID)[0])
	}
}

// All that is owed goes out in one go, at the endpoint's own pace, never
// more at once to one endpoint than allowed; and an endpoint that hangs
// does not keep another waiting.
func TestABacklogDrainsAndAHungEndpointHoldsUpNobody(t *testing.T) {
	r, fast := newRig(t, true), newReceiver(t)
	var atOnce, most atomic.Int32
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		if n := atOnce.Add(1); n > most.Load() {
			most.Store(n)
		}
		<-release
		atOnce.Add(-1)
	}))
	defer hung.Close()
	ctx := context.Background()
	r.svc.Create(ctx, "", webhook.New{URL: fast.srv.URL})
	slow, _ := r.svc.Create(ctx, "", webhook.New{URL: hung.URL})
	for i := range 12 {
		r.bounce("example.com", fmt.Sprintf("up-%d", i))
	}
	finished := make(chan int)
	go func() { n, _ := r.worker.Pass(ctx); finished <- n }()
	// The fast endpoint gets all twelve while the other still hangs.
	deadline := time.After(5 * time.Second)
	for got := 0; got < 12; {
		select {
		case <-deadline:
			t.Fatalf("the fast endpoint got %d of 12 while the other hung", got)
		case <-time.After(10 * time.Millisecond):
			fast.mu.Lock()
			got = len(fast.got)
			fast.mu.Unlock()
		}
	}
	close(release)
	if n := <-finished; n != 24 || most.Load() != 2 {
		t.Fatalf("%d tries in the pass, %d at once to one endpoint (want 24 and 2)", n, most.Load())
	}
	for _, d := range r.deliveries(slow.ID) {
		if d.State != store.DeliveryDelivered {
			t.Fatalf("left over: %+v", d)
		}
	}
}

// Stopping does not wait out a hanging endpoint: the try is cut short and
// handed back uncounted.
func TestStoppingHandsBackWhatIsInFlight(t *testing.T) {
	r := newRig(t, true)
	arrived, release := make(chan struct{}, 4), make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { arrived <- struct{}{}; <-release }))
	defer hung.Close()
	defer close(release)
	e, _ := r.svc.Create(context.Background(), "", webhook.New{URL: hung.URL})
	r.bounce("example.com", "up-1")
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { r.worker.Run(ctx); close(stopped) }()
	<-arrived
	began := time.Now()
	stop()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the worker did not stop while an endpoint hung")
	}
	if d := r.deliveries(e.ID)[0]; d.State != store.DeliveryPending || d.Attempts != 0 || time.Since(began) > time.Second {
		t.Fatalf("after stopping in %s: %+v", time.Since(began), d)
	}
	if !r.worker.Healthy() {
		t.Fatal("a worker that stopped cleanly is not failing")
	}
}

// The webhook says whether the address is on the list with this event,
// not whether an event of its kind would put it there: a bounce of an
// email sent before a suppression was lifted suppresses nothing.
func TestSuppressedSaysWhatIsSo(t *testing.T) {
	r, app := newRig(t, true), newReceiver(t)
	ctx := context.Background()
	r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL})
	late := store.Start{KeyID: "k", Sender: "app@example.com", Recipient: "ana@example.org", Driver: "fake"}
	c, _ := r.store.Start(ctx, late) // sent before the first bounce is known
	r.store.Sent(ctx, c.ID, "up-late")
	r.bounceOf("up-first", "ana@example.org")
	r.now = r.now.Add(time.Minute)
	if err := r.store.Unsuppress(ctx, "example.com", "ana@example.org"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	r.store.RecordEvent(ctx, store.Event{Driver: "fake", UpstreamEventID: "e-late", UpstreamID: "up-late",
		Kind: store.EventBounced, Permanent: true, Code: "5.1.1", At: r.now})
	r.pass()
	// The two go out at once and arrive in either order; the later event
	// is the one about the email sent before the lift.
	slices.Sort(app.bodies)
	if len(app.bodies) != 2 || !strings.Contains(app.bodies[0], `"suppressed":true`) ||
		!strings.Contains(app.bodies[1], `"suppressed":false`) || !strings.Contains(app.bodies[1], `"type":"Permanent"`) {
		t.Fatalf("%v", app.bodies)
	}
}
