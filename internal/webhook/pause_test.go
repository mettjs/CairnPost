package webhook_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// pausing gives the rig a worker that pauses an endpoint after it has
// failed for a time.
func (r *rig) pausing(after time.Duration) {
	clock := func() time.Time { return r.now }
	r.worker = webhook.NewWorker(r.store, webhook.NewPoster(time.Second, true, 2, "test", clock),
		webhook.Options{Poll: 10 * time.Millisecond, Workers: 4, PerEndpoint: 2, Retries: retries, PauseAfter: after},
		slog.New(slog.NewTextHandler(&r.logged, nil)))
}

// giveUp sends a new bounce to the endpoints and runs every try of it,
// each of which the endpoint refuses.
func (r *rig) giveUp(up string) {
	r.t.Helper()
	r.bounce("example.com", up)
	r.pass()
	for _, wait := range retries {
		r.now = r.now.Add(wait)
		r.pass()
	}
}

// An endpoint that takes nothing for long enough is paused when a webhook
// is given up: nothing more is sent to it, yet it is still owed what
// happens. Resumed, it is sent what it is owed, with what was given up
// while it failed, the first of them included.
func TestAnEndpointThatFailsForLongIsPausedAndResumedLosesNothing(t *testing.T) {
	ctx := context.Background()
	r, app := newRig(t, true), newReceiver(t)
	r.pausing(48 * time.Hour)
	e, _ := r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL})
	app.answer(http.StatusServiceUnavailable)

	began := r.now
	r.giveUp("up-1") // given up a little over an hour in
	if got, _ := r.svc.Get(ctx, "", e.ID); got.PausedAt != nil || got.FailingSince == nil || !got.FailingSince.Equal(began) ||
		webhook.StateOf(got) != webhook.StateOn {
		t.Fatalf("after the first webhook was given up: %+v", got)
	}

	// Two days on, a new webhook's first try misses: that alone does not
	// pause it, since the webhook is tried again.
	r.now = began.Add(49 * time.Hour)
	r.bounce("example.com", "up-2")
	r.pass()
	if got, _ := r.svc.Get(ctx, "", e.ID); got.PausedAt != nil {
		t.Fatal("one miss after a quiet spell paused the endpoint")
	}
	// Its tries all fail too, and it is given up: now it is paused.
	for _, wait := range retries {
		r.now = r.now.Add(wait)
		r.pass()
	}
	got, _ := r.svc.Get(ctx, "", e.ID)
	if got.PausedAt == nil || !got.Enabled || webhook.StateOf(got) != webhook.StatePaused || !strings.Contains(r.logged.String(), "is paused") {
		t.Fatalf("not paused: %+v", got)
	}
	if strings.Contains(r.logged.String(), app.srv.URL) {
		t.Fatal("the log names the endpoint's URL")
	}
	sent := len(app.got)

	// Paused: owed what happens, sent nothing, however long it waits.
	r.bounce("example.com", "up-3")
	r.now = r.now.Add(72 * time.Hour)
	if n := r.pass(); n != 0 || len(app.got) != sent {
		t.Fatalf("a paused endpoint was sent to: %d tries", n)
	}
	if list := r.deliveries(e.ID); len(list) != 3 {
		t.Fatalf("a paused endpoint is owed %d, want 3", len(list))
	}
	// A webhook retried by hand waits too, and changing the endpoint,
	// even to say it is enabled, does not resume it.
	if err := r.svc.Retry(ctx, "", e.ID, r.deliveries(e.ID)[2].ID); err != nil {
		t.Fatal(err)
	}
	on := true
	if got, err := r.svc.Update(ctx, "", e.ID, webhook.Change{Enabled: &on}); err != nil || got.PausedAt == nil || got.FailingSince == nil {
		t.Fatalf("saying it is enabled resumed it: %+v %v", got, err)
	}
	if n := r.pass(); n != 0 {
		t.Fatal("a paused endpoint was sent to")
	}
	// A test that is answered does not resume it either, and keeps when
	// the failing began, which resuming needs.
	app.answer(http.StatusNoContent)
	if res, err := r.svc.Test(ctx, "", e.ID); err != nil || !res.Delivered() {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := r.svc.Get(ctx, "", e.ID); got.PausedAt == nil || got.FailingSince == nil {
		t.Fatalf("a test resumed it, or forgot the failing: %+v", got)
	}

	// Resumed: all three arrive, the first one given up among them.
	got, resumed, err := r.svc.Resume(ctx, "", e.ID)
	if err != nil || !resumed || got.PausedAt != nil || got.FailingSince != nil {
		t.Fatalf("resumed: %+v %v %v", got, resumed, err)
	}
	r.pass()
	for _, d := range r.deliveries(e.ID) {
		if d.State != store.DeliveryDelivered {
			t.Errorf("after resuming, %s is %s", d.ID, d.State)
		}
	}
	// Resuming one that is not paused changes nothing, and another
	// domain's key meets no such endpoint.
	if _, resumed, err := r.svc.Resume(ctx, "", e.ID); err != nil || resumed {
		t.Fatalf("resuming twice: %v %v", resumed, err)
	}
	if _, _, err := r.svc.Resume(ctx, "other.example", e.ID); err != store.ErrNotFound {
		t.Fatalf("a bound caller: %v", err)
	}
}

// An answer ends the failing: one to a webhook, and one to a test. With
// pausing off an endpoint is never paused. A try answered just after the
// endpoint was paused does not lose when its failing began.
func TestAnAnswerEndsTheFailingAndPausingCanBeOff(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		after  time.Duration
		answer func(r *rig, app *receiver, id string)
	}{
		"a webhook answered": {48 * time.Hour, func(r *rig, app *receiver, _ string) {
			app.answer(http.StatusOK)
			r.bounce("example.com", "up-ok")
			r.pass()
		}},
		"a test answered": {48 * time.Hour, func(r *rig, app *receiver, id string) {
			app.answer(http.StatusOK)
			if res, err := r.svc.Test(ctx, "", id); err != nil || !res.Delivered() {
				r.t.Fatalf("%+v %v", res, err)
			}
		}},
		"pausing off": {0, nil},
	} {
		r, app := newRig(t, true), newReceiver(t)
		r.pausing(c.after)
		e, _ := r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL})
		app.answer(http.StatusServiceUnavailable)
		r.giveUp("up-1")
		if c.answer != nil {
			c.answer(r, app, e.ID)
			if got, _ := r.svc.Get(ctx, "", e.ID); got.FailingSince != nil {
				t.Fatalf("%s: still failing after an answer", name)
			}
			app.answer(http.StatusServiceUnavailable)
		}
		r.now = r.now.Add(100 * time.Hour)
		r.giveUp("up-2")
		if got, _ := r.svc.Get(ctx, "", e.ID); got.PausedAt != nil {
			t.Errorf("%s: paused", name)
		}
	}

	// Paused by one try while another to it was in flight and answered:
	// the answer leaves it paused and keeps when the failing began.
	r, app := newRig(t, true), newReceiver(t)
	e, _ := r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL})
	r.bounce("example.com", "up-a")
	r.bounce("example.com", "up-b")
	due, err := r.store.TakeDeliveries(ctx, 10, 10, nil, time.Minute)
	if err != nil || len(due) != 2 {
		t.Fatalf("%d taken: %v", len(due), err)
	}
	if _, err := r.store.DeliveryMissed(ctx, due[0].Delivery.ID, 1, 503, "status", 0, false, time.Hour); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(2 * time.Hour)
	again, _ := r.store.TakeDeliveries(ctx, 10, 10, nil, time.Minute)
	if paused, err := r.store.DeliveryMissed(ctx, again[0].Delivery.ID, 2, 503, "status", 0, true, time.Hour); err != nil || !paused {
		t.Fatalf("paused %v: %v", paused, err)
	}
	if err := r.store.DeliveryDone(ctx, due[1].Delivery.ID, 200); err != nil {
		t.Fatal(err)
	}
	got, _ := r.svc.Get(ctx, "", e.ID)
	if got.PausedAt == nil || got.FailingSince == nil {
		t.Fatalf("an answer after the pause: %+v", got)
	}
	if _, resumed, err := r.svc.Resume(ctx, "", e.ID); err != nil || !resumed {
		t.Fatalf("%v %v", resumed, err)
	}
	if list := r.deliveries(e.ID); list[1].State != store.DeliveryPending {
		t.Fatalf("resuming did not owe again what was given up: %+v", list)
	}
}
