//go:build integration

package postgres_test

import (
	"context"
	"os"
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
	return store.Start{KeyID: k.ID, Sender: "app@example.com", Recipient: "ana@example.org", Driver: "test",
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
