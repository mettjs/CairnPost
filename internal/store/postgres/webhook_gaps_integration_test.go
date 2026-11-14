//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// An endpoint is failing from when its first missed try was made, an
// answer ends that, and one that has failed for long enough is paused
// when a webhook is given up: owed what happens, given nothing. Changing
// it does not resume it; resuming does, and what was given up since the
// failing began, the try that began it included, is owed once more.
func TestAnEndpointIsPausedAfterFailingAndResumedWithWhatItMissed(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	e := endpoint(t, db, store.Endpoint{Domain: domain})
	bounce := func(up string) {
		t.Helper()
		sentFrom(t, db, k, "app@"+domain, up)
		if _, err := db.RecordEvent(ctx, bounced(up, "5.1.1")); err != nil {
			t.Fatal(err)
		}
	}
	miss := func(d store.Due, final bool) bool {
		t.Helper()
		paused, err := db.DeliveryMissed(ctx, d.Delivery.ID, d.Delivery.Attempts, 503, "status", 0, final, 48*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return paused
	}
	read := func() store.Endpoint {
		t.Helper()
		got, err := db.Endpoint(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	bounce("<p1-" + k.ID + ">")
	first := take(t, db, 10, e)
	if len(first) != 1 || miss(first[0], false) || read().FailingSince == nil || read().PausedAt != nil {
		t.Fatalf("after the first miss: %+v", read())
	}
	// An answer ends the failing; so does a test that is answered.
	again := take(t, db, 10, e)
	if err := db.DeliveryDone(ctx, again[0].Delivery.ID, 204); err != nil || read().FailingSince != nil {
		t.Fatalf("after an answer: %+v %v", read().FailingSince, err)
	}
	bounce("<p2-" + k.ID + ">")
	given := take(t, db, 10, e)
	if miss(given[0], true) {
		t.Fatal("paused by a webhook given up on the first day")
	}
	// Given up by an endpoint that is not paused, it is owed no more.
	if owed, err := db.Owed(ctx, []string{e.ID}, 100); err != nil || len(owed) != 0 {
		t.Fatalf("owed after a webhook was given up: %v %v", owed, err)
	}
	if err := db.EndpointAnswered(ctx, e.ID); err != nil || read().FailingSince != nil {
		t.Fatalf("after a test was answered: %+v %v", read().FailingSince, err)
	}

	// Failing again: the failing began when the try was made, not when
	// its miss was written down, so the try that began it counts.
	exec(t, `UPDATE webhook_deliveries SET state = 'pending', next_at = now(), taken_until = NULL WHERE id = $1::uuid`, given[0].Delivery.ID)
	began := take(t, db, 10, e)
	exec(t, `UPDATE webhook_deliveries SET attempted_at = now() - interval '3 days' WHERE id = $1::uuid`, began[0].Delivery.ID)
	if miss(began[0], false) {
		t.Fatal("a miss that is not final paused it")
	}
	// Its next try is not yet due, so the next take is the new webhook's.
	exec(t, `UPDATE webhook_deliveries SET next_at = now() + interval '1 hour' WHERE id = $1::uuid`, began[0].Delivery.ID)
	if since := read().FailingSince; since == nil || time.Since(*since) < 71*time.Hour {
		t.Fatalf("the failing began %v, want when the try was made, three days ago", since)
	}
	// Three days into the failing, a first miss does not pause it; the
	// webhook given up does.
	bounce("<p3-" + k.ID + ">")
	third := take(t, db, 10, e)
	if len(third) != 1 || miss(third[0], false) || read().PausedAt != nil {
		t.Fatal("one miss paused it, though its webhook is still to be tried again")
	}
	exec(t, `UPDATE webhook_deliveries SET next_at = now() + interval '1 hour' WHERE id = $1::uuid`, third[0].Delivery.ID)
	due(t, db, began[0].Delivery.ID)
	last := take(t, db, 10, e)
	if len(last) != 1 || !miss(last[0], true) || read().PausedAt == nil || !read().Enabled {
		t.Fatalf("not paused by the webhook given up: %+v", read())
	}
	// Paused: owed what happens, given nothing; an answer to a try that
	// was in flight keeps it paused and keeps when its failing began.
	bounce("<p4-" + k.ID + ">")
	if got := take(t, db, 10, e); len(got) != 0 {
		t.Fatalf("%d taken for a paused endpoint", len(got))
	}
	if list, err := db.ListDeliveries(ctx, e.ID, "", "", 10); err != nil || len(list) != 4 {
		t.Fatalf("a paused endpoint is owed %d, want 4: %v", len(list), err)
	}
	on := true
	if got, err := db.UpdateEndpoint(ctx, e.ID, store.EndpointChange{Enabled: &on}); err != nil || got.PausedAt == nil || got.FailingSince == nil {
		t.Fatalf("a change resumed it: %+v %v", got, err)
	}

	// What it is said to be owed is what resuming it sends: the two that
	// wait and the one given up while it failed, not the one it took.
	owed, err := db.Owed(ctx, []string{e.ID, "nothing"}, 100)
	if err != nil || len(owed) != 1 || owed[e.ID] != 3 {
		t.Fatalf("a paused endpoint is said to be owed %v, want 3: %v", owed, err)
	}

	if capped, err := db.Owed(ctx, []string{e.ID}, 2); err != nil || capped[e.ID] != 2 {
		t.Fatalf("counting to 2 at most said %v: %v", capped, err)
	}

	// Resumed: nothing is failing, and the one given up is owed again.
	got, resumed, err := db.ResumeEndpoint(ctx, e.ID)
	if err != nil || !resumed || got.PausedAt != nil || got.FailingSince != nil {
		t.Fatalf("resumed: %+v %v %v", got, resumed, err)
	}
	if list, _ := db.ListDeliveries(ctx, e.ID, store.DeliveryFailed, "", 10); len(list) != 0 {
		t.Fatalf("still given up after resuming: %+v", list)
	}
	if after, err := db.Owed(ctx, []string{e.ID}, 100); err != nil || after[e.ID] != owed[e.ID] {
		t.Fatalf("owed %v once resumed, %v before: %v", after, owed, err)
	}
	exec(t, `UPDATE webhook_deliveries SET next_at = now() - interval '1 second' WHERE endpoint_id = $1::uuid AND state = 'pending'`, e.ID)
	if got := take(t, db, 10, e); len(got) != owed[e.ID] {
		t.Fatalf("%d taken after resuming, want the three it is owed", len(got))
	}
	if _, resumed, err := db.ResumeEndpoint(ctx, e.ID); err != nil || resumed {
		t.Fatalf("resuming one that is not paused: %v %v", resumed, err)
	}
	if _, _, err := db.ResumeEndpoint(ctx, "nothing"); err != store.ErrNotFound {
		t.Fatalf("no such endpoint: %v", err)
	}
}

// A changed signing key is kept beside the ones before for their
// overlap, each for its own, so a second change does not cut off the
// first key; the ones before are answered, and taken with a delivery,
// only that long; then they are forgotten.
func TestASigningKeyIsChangedWithAnOverlap(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	e := endpoint(t, db, store.Endpoint{Domain: domain})
	second, third := []byte("the key after the change"), []byte("the key after the second change")
	got, err := db.RotateSecret(ctx, e.ID, second, time.Hour)
	if err != nil || !bytes.Equal(got.Secret, second) || len(got.Previous) != 1 || !bytes.Equal(got.Previous[0], e.Secret) || got.PreviousUntil == nil {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = db.RotateSecret(ctx, e.ID, third, 2*time.Hour)
	if err != nil || !bytes.Equal(got.Secret, third) || len(got.Previous) != 2 || !bytes.Equal(got.Previous[0], second) ||
		!bytes.Equal(got.Previous[1], e.Secret) || time.Until(*got.PreviousUntil) < 119*time.Minute {
		t.Fatalf("after a second change: %+v %v", got, err)
	}
	up := "<r1-" + k.ID + ">"
	sentFrom(t, db, k, "app@"+domain, up)
	if _, err := db.RecordEvent(ctx, bounced(up, "5.1.1")); err != nil {
		t.Fatal(err)
	}
	taken := take(t, db, 10, e)
	if len(taken) != 1 || !bytes.Equal(taken[0].Endpoint.Secret, third) || len(taken[0].Endpoint.Previous) != 2 {
		t.Fatalf("within the overlaps: %+v", taken)
	}
	// The first key's overlap over: it is neither answered nor taken,
	// and the second still is.
	exec(t, `UPDATE webhook_previous_secrets SET until = now() - interval '1 second' WHERE endpoint_id = $1::uuid AND secret = $2`, e.ID, e.Secret)
	due(t, db, taken[0].Delivery.ID)
	exec(t, `UPDATE webhook_deliveries SET taken_until = NULL WHERE id = $1::uuid`, taken[0].Delivery.ID)
	later := take(t, db, 10, e)
	if read, _ := db.Endpoint(ctx, e.ID); len(later) != 1 || len(later[0].Endpoint.Previous) != 1 || !bytes.Equal(later[0].Endpoint.Previous[0], second) ||
		len(read.Previous) != 1 || read.PreviousUntil == nil {
		t.Fatalf("after the first overlap: %+v", later)
	}
	// Only keys whose time is over are forgotten: what any pruner takes.
	for {
		n, err := db.ForgetSecrets(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n < 1000 {
			break
		}
	}
	conn, err := pgx.Connect(ctx, os.Getenv("CAIRNPOST_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var kept int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM webhook_previous_secrets WHERE endpoint_id = $1::uuid`, e.ID).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("%d old keys kept, want the one still signing: %v", kept, err)
	}
	// With no overlap the key before is not kept at all.
	if got, err = db.RotateSecret(ctx, e.ID, []byte("a fourth"), 0); err != nil || len(got.Previous) != 1 || !bytes.Equal(got.Previous[0], second) {
		t.Fatalf("with no overlap: %+v %v", got, err)
	}
	if _, err := db.RotateSecret(ctx, "nothing", second, time.Hour); err != store.ErrNotFound {
		t.Fatalf("no such endpoint: %v", err)
	}
}

// An address suppressed because of an email with no record here is owed,
// once, to the endpoints that hear about its domain and about such news;
// what is taken names the suppression and no email; and it is pruned by
// its age, since the suppression it tells of is never deleted.
func TestASuppressionWithNoRecordIsOwedOnItsOwn(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	all := endpoint(t, db, store.Endpoint{Domain: domain})
	asked := endpoint(t, db, store.Endpoint{Domain: domain, Kinds: []string{store.KindSuppressed}})
	other := endpoint(t, db, store.Endpoint{Domain: domain, Kinds: []string{store.EventDelivered}})
	e := bounced("<nobody-"+k.ID+">", "5.1.1")
	e.Sender, e.Recipient = "App@"+domain, "Gone@example.org"
	for range 2 {
		if got, err := db.RecordEvent(ctx, e); err != nil || got != store.NoMessage {
			t.Fatalf("%v %v", got, err)
		}
	}
	taken := take(t, db, 10, all, asked, other)
	if len(taken) != 2 {
		t.Fatalf("%d taken, want one each for the two that hear of it", len(taken))
	}
	for _, d := range taken {
		s := d.Suppression
		if d.Endpoint.ID == other.ID || s == nil || s.Domain != domain || s.Address != "gone@example.org" || s.Reason != "bounce" ||
			s.CreatedAt.IsZero() || d.Delivery.Kind != store.KindSuppressed || d.Delivery.MessageID != "" || d.Event.ID != "" || d.Message.ID != "" {
			t.Fatalf("%+v %+v", d, s)
		}
	}
	list, err := db.ListDeliveries(ctx, all.ID, "", "", 10)
	if err != nil || len(list) != 1 || list[0].Kind != store.KindSuppressed || list[0].MessageID != "" {
		t.Fatalf("%+v %v", list, err)
	}
	// What it says is what was so when it was owed: the suppression's own
	// row changing does not change a webhook between tries.
	if err := db.Unsuppress(ctx, domain, "gone@example.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Suppress(ctx, store.Suppression{Domain: domain, Address: "gone@example.org", Reason: store.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	exec(t, `UPDATE webhook_deliveries SET taken_until = NULL, next_at = now() - interval '1 second' WHERE endpoint_id = $1::uuid`, all.ID)
	if again := take(t, db, 10, all); len(again) != 1 || again[0].Suppression.Reason != "bounce" ||
		!again[0].Suppression.CreatedAt.Equal(taken[0].Suppression.CreatedAt) {
		t.Fatalf("on a later try it says something else: %+v", again[0].Suppression)
	}
	// Pruned by its age, on the database's clock. This test's rows are
	// made forty days old and pruned as older than thirty: what else goes
	// was that old too, and any pruner would take it.
	before, err := db.Horizon(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	prune := func() {
		t.Helper()
		for {
			n, err := db.PruneNotices(ctx, before, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if n < 1000 {
				return
			}
		}
	}
	prune()
	if list, _ := db.ListDeliveries(ctx, all.ID, "", "", 10); len(list) != 1 {
		t.Fatal("pruned before its time")
	}
	exec(t, `UPDATE webhook_deliveries SET created_at = created_at - interval '40 days' WHERE endpoint_id IN ($1::uuid, $2::uuid)`, all.ID, asked.ID)
	prune()
	if list, _ := db.ListDeliveries(ctx, all.ID, "", "", 10); len(list) != 0 {
		t.Fatalf("not pruned: %+v", list)
	}
	if sup, err := db.ListSuppressions(ctx, domain, nil, 10); err != nil || len(sup) != 1 {
		t.Fatalf("the suppression went with what told of it: %+v %v", sup, err)
	}
}
