package webhook_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// stranger records a hard bounce of an email this server has no record
// of, as an upstream's event names it.
func (r *rig) stranger(event, to string) {
	r.t.Helper()
	got, err := r.store.RecordEvent(context.Background(), store.Event{Driver: "fake", UpstreamEventID: event,
		UpstreamID: "nobody-knows-" + event, Kind: store.EventBounced, Permanent: true, Code: "5.1.1", At: r.now,
		Sender: "app@example.com", Recipient: to})
	if err != nil || got != store.NoMessage {
		r.t.Fatalf("%v %v", got, err)
	}
}

// An address suppressed because of an email with no record here has no
// event to be told by, so it is told on its own, once, to the endpoints
// that hear about its sending domain and about such news.
func TestASuppressionWithNoEmailOnRecordIsToldOnItsOwn(t *testing.T) {
	ctx := context.Background()
	r, app, other, picky := newRig(t, true), newReceiver(t), newReceiver(t), newReceiver(t)
	e, _ := r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL, Domain: "example.com"})
	r.svc.Create(ctx, "", webhook.New{URL: other.srv.URL, Domain: "other.example"})
	r.svc.Create(ctx, "", webhook.New{URL: picky.srv.URL, Types: []string{"email.delivered"}})
	asked, _ := r.svc.Create(ctx, "", webhook.New{URL: picky.srv.URL + "/asked", Types: []string{webhook.TypeSuppressed}})
	if len(asked.Kinds) != 1 || asked.Kinds[0] != store.KindSuppressed {
		t.Fatalf("an endpoint that asks for it: %+v", asked.Kinds)
	}

	r.stranger("e-1", "Dead@Example.org")
	r.stranger("e-1", "dead@example.org") // the same event, handed over again
	r.stranger("e-2", "dead@example.org") // another bounce of the same address
	r.pass()
	if len(app.got) != 1 || len(other.got) != 0 || len(picky.got) != 1 {
		t.Fatalf("received: %d, another domain's %d, the picky ones %d", len(app.got), len(other.got), len(picky.got))
	}
	want := `{"created_at":"` + r.now.Format(time.RFC3339) + `","data":{"address":"dead@example.org","domain":"example.com","reason":"bounce"},"type":"address.suppressed"}`
	if app.bodies[0] != want {
		t.Fatalf("the webhook:\n got %s\nwant %s", app.bodies[0], want)
	}
	list := r.deliveries(e.ID)
	if len(list) != 1 || list[0].Kind != store.KindSuppressed || list[0].MessageID != "" || list[0].State != store.DeliveryDelivered {
		t.Fatalf("%+v", list)
	}

	// An email on record tells of its suppression in its own webhook, and
	// nothing is sent beside it.
	r.bounceOf("up-1", "gone@example.org")
	r.pass()
	if len(app.got) != 2 || !strings.Contains(app.bodies[1], `"type":"email.bounced"`) || !strings.Contains(app.bodies[1], `"suppressed":true`) {
		t.Fatalf("an email on record: %v", app.bodies[1:])
	}

	// A suppression lifted is not brought back by an email with no
	// record, so there is nothing to tell.
	if err := r.store.Unsuppress(ctx, "example.com", "dead@example.org"); err != nil {
		t.Fatal(err)
	}
	r.stranger("e-3", "dead@example.org")
	if n := r.pass(); n != 0 {
		t.Fatalf("a lifted suppression was told of again: %d", n)
	}

	// What told of it is forgotten with the rest that is that old.
	if n, err := r.store.PruneNotices(ctx, r.now, 10); err != nil || n != 2 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	if list := r.deliveries(e.ID); len(list) != 1 || list[0].Kind != store.EventBounced {
		t.Fatalf("after pruning: %+v", list)
	}
}
