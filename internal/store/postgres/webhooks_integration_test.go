//go:build integration

package postgres_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
)

// endpoint makes an endpoint that is removed when the test ends: the tests
// share a database, and an endpoint left behind would be owed their events.
func endpoint(t *testing.T, db *postgres.DB, e store.Endpoint) store.Endpoint {
	t.Helper()
	e.URL, e.Secret = "http://127.0.0.1:1/hook", []byte("a signing key of the test's own")
	out, err := db.CreateEndpoint(context.Background(), e, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DeleteEndpoint(context.Background(), out.ID) })
	return out
}

// take takes what is due and answers what is this test's endpoints'.
func take(t *testing.T, db *postgres.DB, perEndpoint int, mine ...store.Endpoint) []store.Due {
	t.Helper()
	all, err := db.TakeDeliveries(context.Background(), 1000, perEndpoint, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Due
	for _, d := range all {
		if slices.ContainsFunc(mine, func(e store.Endpoint) bool { return e.ID == d.Endpoint.ID }) {
			out = append(out, d)
		}
	}
	return out
}

// due makes a delivery due again, by moving its time instead of waiting.
func due(t *testing.T, db *postgres.DB, id string) {
	t.Helper()
	exec(t, `UPDATE webhook_deliveries SET next_at = now() - interval '1 second' WHERE id = $1::uuid`, id)
}

// An event recorded is owed, once, to each enabled endpoint that hears
// about its domain and kind; one taken is counted, leased, and carries all
// a try needs.
func TestAnEventIsOwedToTheEndpointsThatHearAboutIt(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	all := endpoint(t, db, store.Endpoint{Domain: domain, Description: "all"})
	bounces := endpoint(t, db, store.Endpoint{Domain: domain, Kinds: []string{store.EventBounced}})
	other := endpoint(t, db, store.Endpoint{Domain: "other-" + domain})
	off := endpoint(t, db, store.Endpoint{Domain: domain})
	no := false
	if _, err := db.UpdateEndpoint(ctx, off.ID, store.EndpointChange{Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	up := "<wh-" + k.ID + ">"
	id := sentFrom(t, db, k, "app@"+domain, up)
	delivered := store.Event{Driver: "test", UpstreamEventID: "d-" + up, UpstreamID: up, Kind: store.EventDelivered, At: time.Now()}
	for range 2 { // handed over twice: owed once
		if _, err := db.RecordEvent(ctx, delivered); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.RecordEvent(ctx, bounced(up, "5.1.1")); err != nil {
		t.Fatal(err)
	}
	got := take(t, db, 10, all, bounces, other, off)
	if len(got) != 3 {
		t.Fatalf("%d deliveries taken, want the two events for one endpoint and the bounce for the other: %+v", len(got), got)
	}
	for _, d := range got {
		if d.Endpoint.ID == bounces.ID && d.Event.Kind != store.EventBounced {
			t.Errorf("an endpoint that hears bounces was owed a %s", d.Event.Kind)
		}
		if d.Delivery.Attempts != 1 || d.Message.ID != id || d.Message.Recipient == "" || d.Endpoint.URL == "" ||
			len(d.Endpoint.Secret) == 0 || d.Event.At.IsZero() {
			t.Errorf("a delivery taken lacks what a try needs: %+v", d)
		}
	}
	if again := take(t, db, 10, all, bounces); len(again) != 0 {
		t.Fatalf("taken twice within the lease: %+v", again)
	}
	// The bounce left the address on the list, and the delivery says so.
	for _, d := range got {
		if d.Event.Suppressed != (d.Event.Kind == store.EventBounced) {
			t.Errorf("a %s taken says suppressed %v", d.Event.Kind, d.Event.Suppressed)
		}
	}
	// A taker that died leaves them to come due again; an endpoint with
	// tries in flight is passed over.
	exec(t, `UPDATE webhook_deliveries SET taken_until = now() - interval '1 second' WHERE endpoint_id IN ($1::uuid, $2::uuid)`, all.ID, bounces.ID)
	busy, err := db.TakeDeliveries(ctx, 1000, 10, []string{all.ID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range busy {
		if d.Endpoint.ID == all.ID {
			t.Fatal("taken for an endpoint named as busy")
		}
		if d.Endpoint.ID == bounces.ID && d.Delivery.Attempts != 2 {
			t.Fatalf("a delivery come due again is its second try: %+v", d.Delivery)
		}
	}
}

// What a try met is settled: done for good, tried again later, or given up;
// and a delivery given up can be made pending again by hand.
func TestADeliveryIsSettledAndCanBeRetried(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	e := endpoint(t, db, store.Endpoint{Domain: domain})
	ups := []string{"<s1-" + k.ID + ">", "<s2-" + k.ID + ">", "<s3-" + k.ID + ">"}
	for _, up := range ups {
		sentFrom(t, db, k, "app@"+domain, up)
		if _, err := db.RecordEvent(ctx, bounced(up, "5.1.1")); err != nil {
			t.Fatal(err)
		}
	}
	// One endpoint with much owed is given only so many at a time.
	first := take(t, db, 2, e)
	if len(first) != 2 {
		t.Fatalf("%d taken with two allowed per endpoint", len(first))
	}
	if err := db.DeliveryDone(ctx, first[0].Delivery.ID, 204); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeliveryMissed(ctx, first[1].Delivery.ID, 7, 503, "status", time.Hour, false, 0); err != nil {
		t.Fatal(err) // a try long overtaken settles nothing
	}
	if _, err := db.DeliveryMissed(ctx, first[1].Delivery.ID, 1, 503, "status", time.Hour, false, 0); err != nil {
		t.Fatal(err)
	}
	rest := take(t, db, 10, e)
	if len(rest) != 1 {
		t.Fatalf("%d taken next, want the one left: the missed one waits an hour", len(rest))
	}
	if _, err := db.DeliveryMissed(ctx, rest[0].Delivery.ID, 1, 0, "timeout", 0, true, 0); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListDeliveries(ctx, e.ID, "", "", 10)
	if err != nil || len(list) != 3 {
		t.Fatalf("%+v %v", list, err)
	}
	states := map[string]store.Delivery{}
	for _, d := range list {
		states[d.State] = d
	}
	if d := states[store.DeliveryDelivered]; d.LastStatus != 204 || d.DeliveredAt == nil {
		t.Errorf("delivered: %+v", d)
	}
	if d := states[store.DeliveryPending]; d.LastStatus != 503 || d.LastFailure != "status" || d.Kind != store.EventBounced || d.MessageID == "" {
		t.Errorf("to be tried again: %+v", d)
	}
	gaveUp := states[store.DeliveryFailed]
	if gaveUp.LastFailure != "timeout" || gaveUp.LastStatus != 0 {
		t.Errorf("given up: %+v", gaveUp)
	}
	due(t, db, gaveUp.ID)
	if got := take(t, db, 10, e); len(got) != 0 {
		t.Fatalf("a delivery given up was taken: %+v", got)
	}
	if err := db.RetryDelivery(ctx, e.ID, states[store.DeliveryDelivered].ID); err != store.ErrNotFound {
		t.Fatalf("retrying what was delivered: %v", err)
	}
	if err := db.RetryDelivery(ctx, e.ID, gaveUp.ID); err != nil {
		t.Fatal(err)
	}
	again := take(t, db, 10, e)
	if len(again) != 1 || again[0].Delivery.Attempts != 2 {
		t.Fatalf("after a retry by hand, one more try: %+v", again)
	}
	// Cut short, the try is handed back uncounted and is due at once; and
	// a webhook the endpoint took is delivered whatever was said meanwhile.
	if err := db.DeliveryReleased(ctx, gaveUp.ID, 2); err != nil {
		t.Fatal(err)
	}
	if back := take(t, db, 10, e); len(back) != 1 || back[0].Delivery.Attempts != 2 {
		t.Fatalf("handed back: %+v", back)
	}
	db.DeliveryMissed(ctx, gaveUp.ID, 2, 0, "timeout", 0, true, 0)
	if err := db.DeliveryDone(ctx, gaveUp.ID, 200); err != nil {
		t.Fatal(err)
	}
	// The list by page and by state, newest first.
	page, err := db.ListDeliveries(ctx, e.ID, "", "", 2)
	if err != nil || len(page) != 2 || page[0].ID <= page[1].ID {
		t.Fatalf("a page: %+v %v", page, err)
	}
	older, err := db.ListDeliveries(ctx, e.ID, "", page[1].ID, 2)
	if err != nil || len(older) != 1 || older[0].ID >= page[1].ID {
		t.Fatalf("the page after: %+v %v", older, err)
	}
	if done, err := db.ListDeliveries(ctx, e.ID, store.DeliveryDelivered, "", 10); err != nil || len(done) != 2 {
		t.Fatalf("those delivered: %+v %v", done, err)
	}
	// A waiting delivery, retried by hand, is due now.
	if err := db.RetryDelivery(ctx, e.ID, states[store.DeliveryPending].ID); err != nil {
		t.Fatal(err)
	}
	if now := take(t, db, 10, e); len(now) != 1 || now[0].Delivery.ID != states[store.DeliveryPending].ID {
		t.Fatalf("a waiting delivery sent now: %+v", now)
	}
	db.DeliveryMissed(ctx, states[store.DeliveryPending].ID, 2, 503, "status", time.Hour, false, 0)
	// A disabled endpoint's deliveries wait, and go on when it is enabled.
	off, on := false, true
	db.UpdateEndpoint(ctx, e.ID, store.EndpointChange{Enabled: &off})
	due(t, db, states[store.DeliveryPending].ID)
	if got := take(t, db, 10, e); len(got) != 0 {
		t.Fatalf("taken for a disabled endpoint: %+v", got)
	}
	db.UpdateEndpoint(ctx, e.ID, store.EndpointChange{Enabled: &on})
	if got := take(t, db, 10, e); len(got) != 1 {
		t.Fatalf("once enabled again: %+v", got)
	}
}

func TestAnEndpointIsKeptChangedAndRemoved(t *testing.T) {
	ctx, db := context.Background(), open(t)
	domain := key(t, db).ID + ".example.com"
	e := endpoint(t, db, store.Endpoint{Domain: "APP." + domain, Kinds: []string{store.EventBounced}, Description: "billing"})
	got, err := db.Endpoint(ctx, e.ID)
	if err != nil || got.Domain != "app."+domain || !got.Enabled || got.Description != "billing" || len(got.Kinds) != 1 ||
		string(got.Secret) == "" {
		t.Fatalf("%+v %v", got, err)
	}
	url, none, blank := "http://127.0.0.1:1/other", []string{}, ""
	got, err = db.UpdateEndpoint(ctx, e.ID, store.EndpointChange{URL: &url, Kinds: &none, Description: &blank})
	if err != nil || got.URL != url || len(got.Kinds) != 0 || got.Description != "" || !got.Enabled {
		t.Fatalf("changed: %+v %v", got, err)
	}
	if list, err := db.ListEndpoints(ctx, "app."+domain); err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if err := db.DeleteEndpoint(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	// So many endpoints may hear about one domain; a full one refuses.
	for i := range 3 {
		made, err := db.CreateEndpoint(ctx, store.Endpoint{URL: url, Domain: "cap." + domain, Secret: []byte("k")}, 2)
		if err == nil {
			t.Cleanup(func() { db.DeleteEndpoint(ctx, made.ID) })
		}
		if full := i == 2; full != (err == store.ErrLimit) {
			t.Fatalf("endpoint %d of a domain allowed 2: %v", i+1, err)
		}
	}
	for what, err := range map[string]error{
		"read":   func() error { _, err := db.Endpoint(ctx, e.ID); return err }(),
		"change": func() error { _, err := db.UpdateEndpoint(ctx, e.ID, store.EndpointChange{}); return err }(),
		"delete": db.DeleteEndpoint(ctx, e.ID),
		"no id":  db.DeleteEndpoint(ctx, "nothing"),
	} {
		if err != store.ErrNotFound {
			t.Errorf("%s of an endpoint that is gone: %v", what, err)
		}
	}
}

// exec runs a statement with a connection of the test's own.
func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("CAIRNPOST_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

// sentFrom records an email the upstream took under an id, to an address
// of the test's own, and answers its id.
func sentFrom(t *testing.T, db *postgres.DB, k store.Key, from, up string) string {
	t.Helper()
	s := start(k, "", "")
	s.Sender, s.Recipient = from, "to-"+up+"@example.org"
	c, err := db.Start(context.Background(), s)
	if err != nil || c.Outcome != store.Started {
		t.Fatalf("the send did not start: %v %v", c.Outcome, err)
	}
	if err := db.Sent(context.Background(), c.ID, up); err != nil {
		t.Fatal(err)
	}
	return c.ID
}
