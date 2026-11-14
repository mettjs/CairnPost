//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
	"github.com/mettjs/cairnpost/migrations"
)

// open connects to the test database and brings its schema up to date. With
// no database named, the test fails rather than skips: a run that asked for
// integration tests and ran none must not read as green.
func open(t *testing.T) *postgres.DB {
	t.Helper()
	dsn := os.Getenv("CAIRNPOST_TEST_DSN")
	if dsn == "" {
		t.Fatal("set CAIRNPOST_TEST_DSN to a scratch database to run the integration tests")
	}
	db, err := postgres.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db
}

func key(t *testing.T, db *postgres.DB) store.Key {
	t.Helper()
	_, k, err := keys.New(db).Create(context.Background(), t.Name(), store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func start(k store.Key, idem, fingerprint string) store.Start {
	// The recipient is the key's own, and each test makes its own key: the
	// tests share a database, and an address one of them suppresses must
	// not be one another sends to.
	return store.Start{KeyID: k.ID, Sender: "app@example.com", Recipient: "ana-" + k.ID + "@example.org", Driver: "test",
		IdemKey: idem, Fingerprint: []byte(fingerprint), TTL: time.Hour, InDoubt: time.Hour}
}

// age makes a key's row two hours older, with a connection of the test's
// own: the test moves the past instead of waiting for it, so no outcome
// depends on how fast the machine is.
func age(t *testing.T, k store.Key, idem string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("CAIRNPOST_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, `
		UPDATE idempotency_keys
		SET created_at = created_at - interval '2 hours', tried_at = tried_at - interval '2 hours'
		WHERE api_key_id = $1::uuid AND key = $2`, k.ID, idem)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("ageing the key: %v, %d rows", err, tag.RowsAffected())
	}
}

func TestMigrateIsDoneOnce(t *testing.T) {
	db := open(t)
	again, err := db.Migrate(context.Background(), migrations.FS)
	if err != nil || len(again) != 0 {
		t.Fatalf("a second run applied %v, err %v", again, err)
	}
}

// The promise an Idempotency-Key makes rests on the table's primary key:
// of many requests racing under one key, one sends.
func TestRacingRequestsUnderOneKeyStartOnce(t *testing.T) {
	db := open(t)
	k := key(t, db)
	const racers = 32
	outcomes := make([]store.Outcome, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := db.Start(context.Background(), start(k, "race", "same"))
			if err != nil {
				t.Error(err)
			}
			outcomes[i] = c.Outcome
		}()
	}
	wg.Wait()
	started := 0
	for _, o := range outcomes {
		switch o {
		case store.Started:
			started++
		case store.InFlight:
		default:
			t.Errorf("a racer was answered %v", o)
		}
	}
	if started != 1 {
		t.Fatalf("%d requests started a send; exactly one may", started)
	}
}

func TestAKeysLifeInTheDatabase(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	check := func(why string, s store.Start, want store.Outcome) store.Claim {
		t.Helper()
		got, err := db.Start(ctx, s)
		if err != nil || got.Outcome != want {
			t.Fatalf("%s: outcome %v (want %v), err %v", why, got.Outcome, want, err)
		}
		return got
	}
	state := func(id string) string {
		t.Helper()
		m, err := db.Message(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return m.State
	}
	first := check("a new key", start(k, "life", "a"), store.Started).ID
	busy := check("while in flight", start(k, "life", "a"), store.InFlight)
	if busy.RetryAfter <= 59*time.Minute || busy.RetryAfter > time.Hour {
		t.Fatalf("the wait said is %s; the window is an hour", busy.RetryAfter)
	}
	// Nothing was sent under the key yet, so a different request cannot be
	// told that something was.
	check("another request while in flight", start(k, "life", "b"), store.InFlight)

	// The answer never comes, and the window passes: the retry sends, and
	// the first message's fate is recorded as unknown.
	if err := db.Lost(ctx, first); err != nil || state(first) != store.StateUnknown {
		t.Fatalf("a lost answer: %v, state %s", err, state(first))
	}
	age(t, k, "life")
	second := check("after the in-doubt window", start(k, "life", "a"), store.Started).ID
	if err := db.Sent(ctx, second, "up-1"); err != nil {
		t.Fatal(err)
	}
	if got := check("once sent", start(k, "life", "a"), store.Replay); got.ID != second {
		t.Fatalf("replayed %s, sent %s", got.ID, second)
	}
	check("another request once sent", start(k, "life", "b"), store.Mismatch)

	// The key's life was renewed by the try that sent: its first try is
	// two hours old, twice the key's lifetime, and it is still remembered.
	check("a life counted from the try that sent", start(k, "life", "a"), store.Replay)
	age(t, k, "life")
	check("once forgotten, even a different request", start(k, "life", "c"), store.Started)

	failed := check("a second key", start(k, "life-2", "a"), store.Started).ID
	if err := db.Failed(ctx, failed); err != nil {
		t.Fatal(err)
	}
	check("after a refusal the key is free", start(k, "life-2", "a"), store.Started)
	if state(failed) != store.StateFailed {
		t.Fatalf("the refused message is %q", state(failed))
	}
	if _, err := db.Message(ctx, "not-a-uuid"); err != store.ErrNotFound {
		t.Fatalf("a malformed id: %v", err)
	}
}

// A key that Postgres could not store as text never reaches it: it is
// refused as the request's own fault. The store is asked directly here to
// show what would happen otherwise.
func TestTheDatabaseRefusesAKeyThatIsNotText(t *testing.T) {
	db := open(t)
	if _, err := db.Start(context.Background(), start(key(t, db), "caf\xe9", "a")); err == nil {
		t.Fatal("Postgres took a key that is not UTF-8; the request check may be relaxed")
	}
}

// sent records a message the upstream took under an id, and answers ours.
func sent(t *testing.T, db *postgres.DB, k store.Key, upstreamID string) string {
	t.Helper()
	// A recipient of this message's own: one test's complaint must not
	// suppress the address another test sends to.
	s := start(k, "", "")
	s.Recipient = fmt.Sprintf("to-%x@example.org", sha256.Sum256([]byte(upstreamID)))
	c, err := db.Start(context.Background(), s)
	if err != nil || c.Outcome != store.Started {
		t.Fatalf("the send did not start: %v %v", c.Outcome, err)
	}
	if err := db.Sent(context.Background(), c.ID, upstreamID); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestAnEventIsKeptOnceAndMovesItsMessageForward(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	up := "<" + k.ID + "@example.com>" // an upstream id no other run has used
	id := sent(t, db, k, up)
	at := time.Now().UTC().Truncate(time.Millisecond)
	event := func(eventID, kind string) store.Event {
		return store.Event{Driver: "test", UpstreamEventID: k.ID + eventID, UpstreamID: up, Kind: kind, At: at}
	}
	record := func(why string, e store.Event, want store.Recorded, state string) {
		t.Helper()
		got, err := db.RecordEvent(ctx, e)
		m, _ := db.Message(ctx, id)
		if err != nil || got != want || m.State != state {
			t.Fatalf("%s: outcome %v (want %v), state %s (want %s), err %v", why, got, want, m.State, state, err)
		}
	}
	record("a delivery", event("1", store.EventDelivered), store.Kept, store.StateDelivered)
	record("the same event again", event("1", store.EventDelivered), store.Duplicate, store.StateDelivered)
	record("a late deferral", event("2", store.EventDeferred), store.Kept, store.StateDelivered)
	bounce := event("3", store.EventComplained)
	bounce.Code, bounce.Reason = "5.1.1", "abuse"
	record("a complaint after delivery", bounce, store.Kept, store.StateComplained)

	stranger := event("4", store.EventDelivered)
	stranger.UpstreamID = "<nobody-" + k.ID + "@example.com>"
	if got, err := db.RecordEvent(ctx, stranger); err != nil || got != store.NoMessage {
		t.Fatalf("an event about no known email: %v %v", got, err)
	}
	// The same upstream id under another driver is another email.
	elsewhere := event("5", store.EventBounced)
	elsewhere.Driver = "another"
	if got, err := db.RecordEvent(ctx, elsewhere); err != nil || got != store.NoMessage {
		t.Fatalf("another driver's event: %v %v", got, err)
	}
	got, err := db.EventsOf(ctx, id)
	if err != nil || len(got) != 3 || got[2].Code != "5.1.1" || got[2].Reason != "abuse" || !got[0].At.Equal(at) {
		t.Fatalf("the message's events: %+v %v", got, err)
	}
}

// Of two outcomes the later stands, in either order of arrival; an email
// that failed after it was taken frees its key; and a message is read with
// its events as one.
func TestOutcomesKeysAndTheRecordAsOne(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	at := time.Now().UTC().Truncate(time.Millisecond)
	for n, order := range [][]string{{store.EventDelivered, store.EventBounced}, {store.EventBounced, store.EventDelivered}} {
		up := fmt.Sprintf("<order-%d-%s@example.com>", n, k.ID)
		id := sent(t, db, k, up)
		for _, kind := range order {
			when := at
			if kind == store.EventBounced {
				when = at.Add(time.Minute) // the bounce happened after the delivery
			}
			if _, err := db.RecordEvent(ctx, store.Event{Driver: "test", UpstreamEventID: up + kind, UpstreamID: up, Kind: kind, At: when}); err != nil {
				t.Fatal(err)
			}
		}
		m, events, err := db.Record(ctx, id)
		if err != nil || m.State != store.StateBounced || len(events) != 2 {
			t.Fatalf("arriving as %v: state %s, %d events, %v", order, m.State, len(events), err)
		}
	}
	claim := start(k, "fails-later", "f")
	first, _ := db.Start(ctx, claim)
	up := "<fails-" + k.ID + "@example.com>"
	if err := db.Sent(ctx, first.ID, up); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordEvent(ctx, store.Event{Driver: "test", UpstreamEventID: up, UpstreamID: up, Kind: store.EventFailed, At: at}); err != nil {
		t.Fatal(err)
	}
	if again, err := db.Start(ctx, claim); err != nil || again.Outcome != store.Started {
		t.Fatalf("a retry after the email failed: %v %v", again.Outcome, err)
	}
	if _, _, err := db.Record(ctx, "not-a-uuid"); err != store.ErrNotFound {
		t.Fatalf("a malformed id: %v", err)
	}
}

// Many readers handing over the same event at once keep it once.
func TestRacingReadersKeepAnEventOnce(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	up := "<race-" + k.ID + "@example.com>"
	id := sent(t, db, k, up)
	const racers = 16
	kept := make([]store.Recorded, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := db.RecordEvent(ctx, store.Event{Driver: "test", UpstreamEventID: "race-" + k.ID,
				UpstreamID: up, Kind: store.EventDelivered, At: time.Now()})
			if err != nil {
				t.Error(err)
			}
			kept[i] = got
		}()
	}
	wg.Wait()
	n := 0
	for _, got := range kept {
		if got == store.Kept {
			n++
		}
	}
	if events, _ := db.EventsOf(ctx, id); n != 1 || len(events) != 1 {
		t.Fatalf("%d readers kept the event, the message has %d", n, len(events))
	}
}

// A bounce for good suppresses the address in the same step as the event,
// and from then on a send that would start to it is refused, in whatever
// way it would have started; a request already sent is still replayed.
func TestASuppressedAddressIsRefusedHoweverTheSendWouldStart(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	// A domain and an address of this run's own.
	from, to := "app@"+k.ID+".example.com", "gone-"+k.ID+"@example.org"
	claim := func(idem, fingerprint string) store.Start {
		s := start(k, idem, fingerprint)
		s.Sender, s.Recipient = from, to
		return s
	}
	outcome := func(why string, s store.Start, want store.Outcome) store.Claim {
		t.Helper()
		c, err := db.Start(ctx, s)
		if err != nil || c.Outcome != want {
			t.Fatalf("%s: %v (want %v), %v", why, c.Outcome, want, err)
		}
		return c
	}
	sentBefore := outcome("before", claim("before", "a"), store.Started)
	up := "<bounce-" + k.ID + "@example.com>"
	if err := db.Sent(ctx, sentBefore.ID, up); err != nil {
		t.Fatal(err)
	}
	inDoubt := outcome("a send left in doubt", claim("doubt", "a"), store.Started)
	bounce := store.Event{Driver: "test", UpstreamEventID: up, UpstreamID: up, Kind: store.EventBounced, Permanent: true, At: time.Now()}
	for range 2 { // handed over twice: one suppression
		if _, err := db.RecordEvent(ctx, bounce); err != nil {
			t.Fatal(err)
		}
	}
	list, err := db.ListSuppressions(ctx, k.ID+".example.com", nil, 10)
	if err != nil || len(list) != 1 || list[0].Address != to || list[0].Reason != store.ReasonBounce || list[0].MessageID != sentBefore.ID {
		t.Fatalf("the suppression: %+v %v", list, err)
	}
	outcome("without a key", claim("", ""), store.Suppressed)
	outcome("under a new key", claim("new", "a"), store.Suppressed)
	outcome("typed in capitals", func() store.Start { s := claim("", ""); s.Recipient = strings.ToUpper(to); return s }(), store.Suppressed)
	if replay := outcome("what was sent before", claim("before", "a"), store.Replay); replay.ID != sentBefore.ID {
		t.Fatalf("replayed %s", replay.ID)
	}
	// Taking over a key whose send was left in doubt would start a send
	// too: refused. The earlier try is recorded as unknown and named in
	// the refusal, since its email may have gone, and the key is let go.
	age(t, k, "doubt")
	if c := outcome("a takeover", claim("doubt", "a"), store.Suppressed); c.ID != inDoubt.ID {
		t.Fatalf("the refused takeover names %q, want the earlier try %q", c.ID, inDoubt.ID)
	}
	if m, _ := db.Message(ctx, inDoubt.ID); m.State != store.StateUnknown {
		t.Fatalf("the refused takeover left the earlier message %q", m.State)
	}
	if c := outcome("the key, let go", claim("doubt", "a"), store.Suppressed); c.ID != "" {
		t.Fatalf("a key let go still names a try: %q", c.ID)
	}
	outcome("from another domain", func() store.Start { s := claim("", ""); s.Sender = "app@other-" + k.ID + ".example.com"; return s }(), store.Started)

	if err := db.Unsuppress(ctx, k.ID+".example.com", strings.ToUpper(to)); err != nil {
		t.Fatal(err)
	}
	if err := db.Unsuppress(ctx, k.ID+".example.com", to); err != store.ErrNotFound {
		t.Fatalf("lifting twice: %v", err)
	}
	outcome("once lifted", claim("new", "a"), store.Started)
}

// bounced is a hard bounce of the email an upstream took under an id.
func bounced(up, code string) store.Event {
	return store.Event{Driver: "test", UpstreamEventID: "evt-" + up, UpstreamID: up, Kind: store.EventBounced,
		Permanent: true, Code: code, At: time.Now()}
}

// A suppression lifted by hand is remembered: a late event about an email
// sent before the lift does not bring it back, one about an email sent
// after it does. A refusal over the sender's standing never suppresses.
func TestALiftIsRememberedAndAPolicyRefusalSuppressesNobody(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain, to := k.ID+".example.com", "gone-"+k.ID+"@example.org"
	send := func(up string) string {
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
	inForce := func(why string, want int) []store.Suppression {
		t.Helper()
		list, err := db.ListSuppressions(ctx, domain, nil, 10)
		if err != nil || len(list) != want {
			t.Fatalf("%s: %d in force, want %d (%v)", why, len(list), want, err)
		}
		return list
	}
	record := func(e store.Event) {
		t.Helper()
		if _, err := db.RecordEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	ups := []string{"<p-" + k.ID + ">", "<a-" + k.ID + ">", "<b-" + k.ID + ">", "<c-" + k.ID + ">"}
	send(ups[0])
	record(bounced(ups[0], "5.7.26"))
	inForce("a policy refusal", 0)
	send(ups[1])
	send(ups[2]) // both sent before anything bounced
	record(bounced(ups[1], "5.1.1"))
	inForce("a hard bounce", 1)
	if n, err := db.UnsuppressAll(ctx, domain, store.ReasonComplaint); err != nil || n != 0 {
		t.Fatalf("lifting another reason's: %d %v", n, err)
	}
	if n, err := db.UnsuppressAll(ctx, domain, ""); err != nil || n != 1 {
		t.Fatalf("lifting the domain's: %d %v", n, err)
	}
	record(bounced(ups[2], "5.1.1"))
	inForce("an email sent before the lift", 0)
	later := send(ups[3])
	record(bounced(ups[3], "5.1.1"))
	if list := inForce("an email sent after the lift", 1); list[0].MessageID != later {
		t.Fatalf("caused by %q, want %q", list[0].MessageID, later)
	}
	// By hand, a lifted one comes back whatever its history.
	if err := db.Unsuppress(ctx, domain, to); err != nil {
		t.Fatal(err)
	}
	if added, err := db.Suppress(ctx, store.Suppression{Domain: domain, Address: to, Reason: store.ReasonManual}); err != nil || !added {
		t.Fatalf("by hand again: %v %v", added, err)
	}
	if list := inForce("by hand", 1); list[0].Reason != store.ReasonManual || list[0].MessageID != "" {
		t.Fatalf("%+v", list[0])
	}
}

// A bounce for good about an email with no record still suppresses, when
// the event names the addresses.
func TestABounceWithNoRecordSuppressesByItsOwnAddresses(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	e := bounced("<nobody-"+k.ID+">", "5.1.1")
	if got, err := db.RecordEvent(ctx, e); err != nil || got != store.NoMessage {
		t.Fatalf("%v %v", got, err)
	}
	e.Sender, e.Recipient = "App@"+domain, "Gone@example.org"
	for range 2 {
		if got, err := db.RecordEvent(ctx, e); err != nil || got != store.NoMessage {
			t.Fatalf("%v %v", got, err)
		}
	}
	list, err := db.ListSuppressions(ctx, domain, nil, 10)
	if err != nil || len(list) != 1 || list[0].Address != "gone@example.org" || list[0].MessageID != "" {
		t.Fatalf("%+v %v", list, err)
	} // When that email was sent is not known, so it never undoes a lift.
	if err := db.Unsuppress(ctx, domain, "gone@example.org"); err != nil {
		t.Fatal(err)
	}
	e.UpstreamEventID, e.At = e.UpstreamEventID+"-late", time.Now().Add(time.Hour)
	if _, err := db.RecordEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if list, err := db.ListSuppressions(ctx, domain, nil, 10); err != nil || len(list) != 0 {
		t.Fatalf("an email with no record undid a lift: %+v %v", list, err)
	}
	// A suppression no message is named for is given the recorded email
	// that bounces next.
	e.Recipient = "new-" + k.ID + "@example.org"
	if _, err := db.RecordEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	s := start(k, "", "")
	s.Sender, s.Recipient = "app@"+domain, "other-"+k.ID+"@example.org"
	c, err := db.Start(ctx, s)
	if err != nil || c.Outcome != store.Started {
		t.Fatalf("%v %v", c.Outcome, err)
	}
	// Recorded as sent to the address that is, by now, suppressed: a send
	// that started just before the first bounce was read.
	conn, err := pgx.Connect(ctx, os.Getenv("CAIRNPOST_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE messages SET recipient = $2 WHERE id = $1::uuid`, c.ID, e.Recipient); err != nil {
		t.Fatal(err)
	}
	up := "<named-" + k.ID + ">"
	if err := db.Sent(ctx, c.ID, up); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordEvent(ctx, bounced(up, "5.1.1")); err != nil {
		t.Fatal(err)
	}
	if list, err := db.ListSuppressions(ctx, domain, nil, 10); err != nil || len(list) != 1 || list[0].MessageID != c.ID {
		t.Fatalf("%+v %v (want message %s)", list, err, c.ID)
	}
}

func TestTheSuppressionListInOrderAndByPage(t *testing.T) {
	ctx, db := context.Background(), open(t)
	domain := key(t, db).ID + ".example.com"
	for _, address := range []string{"c@example.org", "a@example.org", "b@example.org"} {
		if added, err := db.Suppress(ctx, store.Suppression{Domain: domain, Address: address, Reason: store.ReasonManual, Note: "n"}); err != nil || !added {
			t.Fatalf("%s: %v %v", address, added, err)
		}
	}
	if added, err := db.Suppress(ctx, store.Suppression{Domain: domain, Address: "A@example.org", Reason: store.ReasonComplaint}); err != nil || added {
		t.Fatalf("the same mailbox again: %v %v", added, err)
	}
	first, err := db.ListSuppressions(ctx, domain, nil, 2)
	if err != nil || len(first) != 2 || first[0].Address != "a@example.org" || first[0].Reason != store.ReasonManual || first[0].Note != "n" {
		t.Fatalf("%+v %v", first, err)
	}
	rest, err := db.ListSuppressions(ctx, domain, &first[1], 2)
	if err != nil || len(rest) != 1 || rest[0].Address != "c@example.org" {
		t.Fatalf("%+v %v", rest, err)
	}
}

// Events are answered in the order they were recorded, each with its
// email's addresses: the last few, or those after one, of a sending
// domain or of all.
func TestEventsAreReadInTheOrderTheyWereRecorded(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	var ids []string
	for _, n := range []string{"1", "2", "3"} {
		up := "<t" + n + "-" + k.ID + ">"
		ids = append(ids, sentFrom(t, db, k, "app@"+domain, up))
		if _, err := db.RecordEvent(ctx, bounced(up, "5.1.1")); err != nil {
			t.Fatal(err)
		}
	}
	last, err := db.EventsAfter(ctx, "", domain, 2)
	if err != nil || len(last) != 2 || last[0].MessageID != ids[1] || last[1].MessageID != ids[2] ||
		last[1].Sender != "app@"+domain || last[1].Recipient == "" || last[1].Kind != store.EventBounced {
		t.Fatalf("the last two: %+v %v", last, err)
	}
	all, _ := db.EventsAfter(ctx, "", domain, 10)
	after, err := db.EventsAfter(ctx, all[0].ID, domain, 10)
	if err != nil || len(all) != 3 || len(after) != 2 || after[0].ID != all[1].ID {
		t.Fatalf("after the first: %+v %v", after, err)
	}
	if none, err := db.EventsAfter(ctx, all[2].ID, domain, 10); err != nil || len(none) != 0 {
		t.Fatalf("after the last: %+v %v", none, err)
	}
	if other, err := db.EventsAfter(ctx, "", "nothing."+domain, 10); err != nil || len(other) != 0 {
		t.Fatalf("another domain's: %+v %v", other, err)
	}
	if every, err := db.EventsAfter(ctx, all[0].ID, "", 2); err != nil || len(every) != 2 {
		t.Fatalf("of every domain: %+v %v", every, err)
	}
	if odd, err := db.EventsAfter(ctx, "not an id", domain, 10); err != nil || len(odd) != 0 {
		t.Fatalf("after what is no id: %+v %v", odd, err)
	}
}
