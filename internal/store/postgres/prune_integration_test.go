//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// A key remembered long enough is forgotten: its next request sends,
// whatever it is. One whose try never got an answer leaves its message as
// unknown. A key still within its life is kept.
func TestAKeyPastItsLifeIsForgotten(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	begin := func(idem, fingerprint string) store.Claim {
		t.Helper()
		c, err := db.Start(ctx, start(k, idem, fingerprint))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	done, doubt, fresh := begin("done", "one"), begin("doubt", "one"), begin("fresh", "one")
	for _, id := range []string{done.ID, fresh.ID} {
		if err := db.Sent(ctx, id, "<"+id+">"); err != nil {
			t.Fatal(err)
		}
	}
	// The test's keys are made two days old and pruned as older than one:
	// what else goes was past a day too, and any pruner would take it.
	exec(t, `UPDATE idempotency_keys SET created_at = created_at - interval '2 days'
		WHERE api_key_id = $1::uuid AND key IN ('done', 'doubt')`, k.ID)
	for {
		n, err := db.PruneKeys(ctx, 24*time.Hour, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n < 1000 {
			break
		}
	}
	if m, err := db.Message(ctx, doubt.ID); err != nil || m.State != store.StateUnknown {
		t.Fatalf("the try with no answer is %q, want unknown: %v", m.State, err)
	}
	if m, err := db.Message(ctx, done.ID); err != nil || m.State != store.StateSent {
		t.Fatalf("the sent email is %q: %v", m.State, err)
	}
	if c := begin("done", "another request"); c.Outcome != store.Started {
		t.Fatalf("a forgotten key answered %v, want a new send", c.Outcome)
	}
	if c := begin("fresh", "one"); c.Outcome != store.Replay || c.ID != fresh.ID {
		t.Fatalf("a key within its life answered %v", c.Outcome)
	}
}

// An email's record goes with its events and the webhooks owed for them.
// The suppression it caused stays in force, and one that was lifted is
// kept until no email sent before the lift is on record, so a late bounce
// cannot undo the lift.
func TestAnEmailsRecordIsPrunedAndItsSuppressionStays(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	dead, back := "dead-"+k.ID+"@example.org", "back-"+k.ID+"@example.org"
	end := endpoint(t, db, store.Endpoint{Domain: domain, Enabled: true})
	send := func(up, to string) string {
		t.Helper()
		s := start(k, "", "")
		s.Sender, s.Recipient = "app@"+domain, to
		c, err := db.Start(ctx, s)
		if err != nil || c.Outcome != store.Started {
			t.Fatalf("%s did not start: %v %v", up, c.Outcome, err)
		}
		if err := db.Sent(ctx, c.ID, up); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	record := func(e store.Event) {
		t.Helper()
		if _, err := db.RecordEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	inForce := func(why string, want ...string) []store.Suppression {
		t.Helper()
		list, err := db.ListSuppressions(ctx, domain, nil, 10)
		if err != nil || len(list) != len(want) {
			t.Fatalf("%s: %+v in force, want %v (%v)", why, list, want, err)
		}
		for i, s := range list {
			if s.Address != want[i] {
				t.Fatalf("%s: %+v in force, want %v", why, list, want)
			}
		}
		return list
	}
	// The test's emails are made a year and more old, and pruned as older
	// than a year: nothing another test made is that old.
	old := func(id string) {
		t.Helper()
		exec(t, `UPDATE messages SET created_at = created_at - interval '400 days',
			updated_at = updated_at - interval '400 days' WHERE id = $1::uuid`, id)
	}
	prune := func() int {
		t.Helper()
		before, err := db.Horizon(ctx, 365*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.PruneMessages(ctx, before, 1000); err != nil {
			t.Fatal(err)
		}
		n, err := db.PruneLifted(ctx, before, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	ups := []string{"<a-" + k.ID + ">", "<b-" + k.ID + ">", "<c-" + k.ID + ">", "<d-" + k.ID + ">"}
	a, b, c := send(ups[0], dead), send(ups[1], back), send(ups[2], back)
	record(bounced(ups[0], "5.1.1"))
	record(bounced(ups[1], "5.1.1"))
	if list := inForce("two hard bounces", back, dead); list[1].MessageID != a {
		t.Fatalf("caused by %q, want %q", list[1].MessageID, a)
	}
	if err := db.Unsuppress(ctx, domain, back); err != nil {
		t.Fatal(err)
	}
	if owed, err := db.ListDeliveries(ctx, end.ID, "", "", 10); err != nil || len(owed) != 2 {
		t.Fatalf("%d webhooks owed, want 2: %v", len(owed), err)
	}

	for _, id := range []string{a, b, c} {
		old(id)
	}
	prune()
	for _, id := range []string{a, b, c} {
		if _, _, err := db.Record(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a pruned email is still on record: %v", err)
		}
	}
	if events, err := db.EventsOf(ctx, a); err != nil || len(events) != 0 {
		t.Fatalf("a pruned email kept %d events: %v", len(events), err)
	}
	if owed, err := db.ListDeliveries(ctx, end.ID, "", "", 10); err != nil || len(owed) != 0 {
		t.Fatalf("a pruned email's webhooks are still owed: %d %v", len(owed), err)
	}
	if list := inForce("after the prune", dead); list[0].MessageID != "" {
		t.Fatalf("the suppression still names the pruned email: %+v", list[0])
	}
	// The third email was sent before the lift and bounces only now, with
	// its record gone. The lift, made today, is still remembered.
	late := bounced(ups[2], "5.1.1")
	late.Sender, late.Recipient = "app@"+domain, back
	record(late)
	inForce("a late bounce of a pruned email", dead)

	// A year on, the lift is old too. It is kept while an email sent
	// before it is on record, and forgotten once none is. An email sent
	// after it holds nothing back, even one old enough to be due but kept
	// on by an event.
	exec(t, `UPDATE suppressions SET lifted_at = lifted_at - interval '380 days' WHERE domain = $1 AND address = $2`, domain, back)
	d, e := send(ups[3], back), send("<e-"+k.ID+">", "other-"+k.ID+"@example.org")
	old(d)
	exec(t, `UPDATE messages SET created_at = created_at - interval '370 days' WHERE id = $1::uuid`, e)
	t.Cleanup(func() { exec(t, `DELETE FROM messages WHERE id = $1::uuid`, e) })
	before, err := db.Horizon(ctx, 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PruneLifted(ctx, before, 1000); err != nil {
		t.Fatal(err)
	}
	late.UpstreamEventID += "-held"
	record(late)
	inForce("a bounce while an email sent before the lift is on record", dead)
	prune()
	if _, err := db.Message(ctx, e); err != nil {
		t.Fatalf("the email sent after the lift: %v", err)
	}
	late.UpstreamEventID += "-again"
	record(late)
	inForce("a bounce once the lift is forgotten", back, dead)
	prune()
	inForce("suppressions in force are never pruned", back, dead)
}

// An email is kept from the last thing that happened to it, and while an
// Idempotency-Key still points at it.
func TestAnEmailWithAKeyOrALateEventIsKept(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	send := func(idem, up string) string {
		t.Helper()
		c, err := db.Start(ctx, start(k, idem, "one"))
		if err != nil || c.Outcome != store.Started {
			t.Fatalf("%s did not start: %v %v", up, c.Outcome, err)
		}
		if err := db.Sent(ctx, c.ID, up); err != nil {
			t.Fatal(err)
		}
		exec(t, `UPDATE messages SET created_at = created_at - interval '400 days',
			updated_at = updated_at - interval '400 days' WHERE id = $1::uuid`, c.ID)
		return c.ID
	}
	kept := func(why, id string, want bool) {
		t.Helper()
		before, err := db.Horizon(ctx, 365*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.PruneMessages(ctx, before, 1000); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Message(ctx, id); (err == nil) != want {
			t.Fatalf("%s: kept is %v, want %v (%v)", why, err == nil, want, err)
		}
	}
	keyed := send("held", "<keyed-"+k.ID+">")
	late := send("", "<late-"+k.ID+">")
	plain := send("", "<plain-"+k.ID+">")
	e := bounced("<late-"+k.ID+">", "4.2.2")
	e.Kind, e.Permanent = store.EventDeferred, false
	if _, err := db.RecordEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	// An email this old, left on record, would hold back every lifted
	// suppression in the database the tests share.
	t.Cleanup(func() { exec(t, `DELETE FROM messages WHERE id = ANY($1::uuid[])`, []string{keyed, late}) })
	kept("an email with nothing since", plain, false)
	kept("an email a key points at", keyed, true)
	kept("an email with an event today", late, true)
	exec(t, `DELETE FROM idempotency_keys WHERE message_id = $1::uuid`, keyed)
	kept("the same email, its key gone", keyed, false)
}

// What an email said is kept with its message in the one step, as it
// came, whatever bytes it holds; it is answered only within its time, and
// pruned by its own age.
func TestWhatAnEmailSaidIsKeptWithItAndForgottenOnTime(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	words := []byte("{\"subject\":\"a NUL \x00 and \xff are kept as they came\"}")
	s := start(k, "worded", "one")
	asked := 0
	s.Content, s.KeepContent = func() []byte { asked++; return words }, time.Hour
	c, err := db.Start(ctx, s)
	if err != nil || c.Outcome != store.Started {
		t.Fatalf("%v %v", c.Outcome, err)
	}
	plain, err := db.Start(ctx, start(k, "plain", "one"))
	if err != nil {
		t.Fatal(err)
	}
	got, until, err := db.Content(ctx, c.ID, time.Hour)
	if err != nil || string(got) != string(words) || time.Until(until) < 59*time.Minute || time.Until(until) > time.Hour {
		t.Fatalf("%q until %s: %v", got, until, err)
	}
	if got, _, err := db.Content(ctx, plain.ID, time.Hour); err != nil || got != nil {
		t.Fatalf("an email sent without keeping its words has %q: %v", got, err)
	}
	// A request that loses the race for its key is rolled back whole: no
	// words are left behind without a message.
	// And a request that does not start a send is never asked for its
	// words.
	if again, err := db.Start(ctx, s); err != nil || again.Outcome != store.InFlight || asked != 1 {
		t.Fatalf("%v %v, asked for its words %d times", again.Outcome, err, asked)
	}
	// One who keeps for less is not answered what a longer setting kept.
	if got, _, err := db.Content(ctx, c.ID, time.Nanosecond); err != nil || got != nil {
		t.Fatalf("kept an hour, answered to one who keeps none: %q %v", got, err)
	}
	// What a receiving server said is kept until its email's words are,
	// as it came, and handed to no webhook; one that comes later is not.
	end := endpoint(t, db, store.Endpoint{Enabled: true})
	up := "<said-" + k.ID + ">"
	if err := db.Sent(ctx, c.ID, up); err != nil {
		t.Fatal(err)
	}
	e := bounced(up, "5.1.1")
	e.Detail, e.KeepDetail = "550 a NUL \x00 and \xff too", time.Hour
	if got, err := db.RecordEvent(ctx, e); err != nil || got != store.Kept {
		t.Fatalf("an event with a sentence a text column would refuse: %v %v", got, err)
	}
	said, err := db.Details(ctx, c.ID, time.Hour)
	if err != nil || len(said) != 1 {
		t.Fatalf("%v %v", said, err)
	}
	for _, sentence := range said {
		if sentence != e.Detail {
			t.Fatalf("kept as %q", sentence)
		}
	}
	for _, d := range take(t, db, 10, end) {
		if d.Event.Detail != "" {
			t.Fatalf("a webhook was handed %q", d.Event.Detail)
		}
	}
	// Two hours on, neither is answered, even before it is pruned; then
	// both are pruned, and the record stays. A sentence that comes now is
	// not kept at all.
	exec(t, `UPDATE messages SET created_at = created_at - interval '2 hours' WHERE id = $1::uuid`, c.ID)
	exec(t, `UPDATE message_contents SET created_at = created_at - interval '2 hours', expires_at = expires_at - interval '2 hours'
		WHERE message_id = $1::uuid`, c.ID)
	exec(t, `UPDATE event_details SET expires_at = expires_at - interval '2 hours'
		WHERE event_id IN (SELECT id FROM events WHERE message_id = $1::uuid)`, c.ID)
	if got, _, err := db.Content(ctx, c.ID, 1000*time.Hour); err != nil || got != nil {
		t.Fatalf("past its time, still answered: %q %v", got, err)
	}
	if said, err := db.Details(ctx, c.ID, 1000*time.Hour); err != nil || len(said) != 0 {
		t.Fatalf("past its time, still answered: %v %v", said, err)
	}
	left := func() (n int) {
		t.Helper()
		conn, err := pgx.Connect(ctx, os.Getenv("CAIRNPOST_TEST_DSN"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		if err := conn.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM message_contents WHERE message_id = $1::uuid) +
			       (SELECT count(*) FROM event_details d JOIN events e ON e.id = d.event_id WHERE e.message_id = $1::uuid)`,
			c.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if left() != 2 {
		t.Fatalf("%d rows to prune, want 2", left())
	}
	if _, err := db.PruneContents(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PruneDetails(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	late := bounced(up, "5.1.1")
	late.UpstreamEventID, late.Detail, late.KeepDetail = late.UpstreamEventID+"-late", "said too late", time.Hour
	if _, err := db.RecordEvent(ctx, late); err != nil {
		t.Fatal(err)
	}
	if left() != 0 {
		t.Fatalf("%d rows left after the prune", left())
	}
	if _, err := db.Message(ctx, c.ID); err != nil {
		t.Fatalf("the record went with its words: %v", err)
	}
}
