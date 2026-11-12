//go:build integration

package postgres_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
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

func key(t *testing.T, db *postgres.DB) (string, store.Key) {
	t.Helper()
	full, k, err := keys.New(db).Create(context.Background(), t.Name(), store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	return full, k
}

func start(k store.Key, idem, fingerprint string) store.Start {
	return store.Start{KeyID: k.ID, Sender: "app@example.com", Recipient: "ana@example.org", Driver: "test",
		IdemKey: idem, Fingerprint: []byte(fingerprint), TTL: time.Hour, InDoubt: time.Hour}
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
	_, k := key(t, db)
	const racers = 32
	outcomes := make([]store.Outcome, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, err := db.Start(context.Background(), start(k, "race", "same"))
			if err != nil {
				t.Error(err)
			}
			outcomes[i] = o
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
	_, k := key(t, db)
	check := func(why string, s store.Start, want store.Outcome) string {
		t.Helper()
		got, id, err := db.Start(ctx, s)
		if err != nil || got != want {
			t.Fatalf("%s: outcome %v (want %v), err %v", why, got, want, err)
		}
		return id
	}
	first := check("a new key", start(k, "life", "a"), store.Started)
	check("while in flight", start(k, "life", "a"), store.InFlight)
	check("another request", start(k, "life", "b"), store.Mismatch)

	// The answer never comes, and the window passes: the retry sends, and
	// the first message's fate is recorded as unknown.
	stale := start(k, "life", "a")
	stale.InDoubt = time.Millisecond
	time.Sleep(20 * time.Millisecond)
	second := check("after the in-doubt window", stale, store.Started)
	if m, _ := db.Message(ctx, first); m.State != store.StateUnknown {
		t.Fatalf("the abandoned message is %q", m.State)
	}
	if err := db.Sent(ctx, second, "up-1"); err != nil {
		t.Fatal(err)
	}
	if id := check("once sent", start(k, "life", "a"), store.Replay); id != second {
		t.Fatalf("replayed %s, sent %s", id, second)
	}
	forgotten := start(k, "life", "c")
	forgotten.TTL = time.Millisecond
	check("once forgotten, even a different request", forgotten, store.Started)

	failed := check("a second key", start(k, "life-2", "a"), store.Started)
	if err := db.Failed(ctx, failed); err != nil {
		t.Fatal(err)
	}
	check("after a refusal the key is free", start(k, "life-2", "a"), store.Started)
	if m, _ := db.Message(ctx, failed); m.State != store.StateFailed {
		t.Fatalf("the refused message is %q", m.State)
	}
	if _, err := db.Message(ctx, "not-a-uuid"); err != store.ErrNotFound {
		t.Fatalf("a malformed id: %v", err)
	}
}

// No table holds a subject or a body, whatever is sent.
func TestNothingOfAnEmailsWordsReachesTheDatabase(t *testing.T) {
	ctx, db := context.Background(), open(t)
	full, _ := key(t, db)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	drv := capture.New(5, time.Now)
	h := api.New(api.Deps{
		Mail: mail.New(db, drv, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
			InDoubtWindow: time.Minute}, log),
		Keys: keys.New(db), Ping: db.Ping, Driver: "capture", MaxBodyBytes: 1 << 20, Log: log,
	})
	body := `{"from":"App <app@example.com>","to":"ana@example.org","subject":"SUBJECTMARK 482913",
		"text":"TEXTMARK 482913","html":"<p>HTMLMARK</p>","headers":{"X-Trace":"HEADERMARK"}}`
	req := httptest.NewRequest("POST", "/emails", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+full)
	req.Header.Set("Idempotency-Key", "words")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || len(drv.Held()) != 1 {
		t.Fatalf("the send: %d %s", rec.Code, rec.Body)
	}
	dump, err := db.DumpForTest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dump, "ana@example.org") {
		t.Fatal("the dump does not hold the message's row; the check proves nothing")
	}
	secret := full[strings.LastIndex(full, "_")+1:]
	for _, mark := range []string{"SUBJECTMARK", "TEXTMARK", "HTMLMARK", "HEADERMARK", "482913", secret} {
		if strings.Contains(dump, mark) {
			t.Errorf("the database holds %q", mark)
		}
	}
}
