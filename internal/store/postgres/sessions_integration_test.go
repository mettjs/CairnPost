//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
)

// A console session is its key's for a time: it is found by its hash
// until that time is up, it is deleted, or its key is revoked.
func TestAConsoleSessionLivesAndEnds(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	hash := func(token string) []byte { sum := sha256.Sum256([]byte(token + k.ID)); return sum[:] }
	start := func(token string) {
		t.Helper()
		if err := db.CreateSession(ctx, hash(token), k.ID, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	live := func(token string) bool {
		t.Helper()
		got, until, err := db.SessionKey(ctx, hash(token), time.Hour)
		if errors.Is(err, store.ErrNotFound) {
			return false
		}
		if err != nil || got.ID != k.ID || got.Scope != store.ScopeAdmin || time.Until(until) > time.Hour || time.Until(until) < 59*time.Minute {
			t.Fatalf("%+v until %s: %v", got, until, err)
		}
		return true
	}
	start("old")
	start("out")
	start("kept")
	if !live("old") || live("never") {
		t.Fatal("a session just started is not found, or one never started is")
	}
	exec(t, `UPDATE console_sessions SET expires_at = now() - interval '1 second' WHERE hash = $1`, hash("old"))
	if live("old") {
		t.Fatal("a session past its time is still found")
	}
	if err := db.DeleteSession(ctx, hash("out")); err != nil || live("out") {
		t.Fatalf("a deleted session is still found: %v", err)
	}
	if err := db.DeleteSession(ctx, hash("out")); err != nil {
		t.Fatalf("deleting it twice: %v", err)
	}
	// What a session looks for is kept with it, and is its own.
	if err := db.LookFor(ctx, hash("kept"), "ana@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := db.LookFor(ctx, hash("never"), "bo@example.org"); err != nil {
		t.Fatalf("looking for something in no session: %v", err)
	}
	for token, want := range map[string]string{"kept": "ana@example.org", "old": "", "never": "", "out": ""} {
		if got, err := db.LookingFor(ctx, hash(token)); err != nil || got != want {
			t.Fatalf("session %s looks for %q: %v", token, got, err)
		}
	}
	if err := db.LookFor(ctx, hash("kept"), ""); err != nil {
		t.Fatal(err)
	}
	if got, err := db.LookingFor(ctx, hash("kept")); err != nil || got != "" {
		t.Fatalf("after looking for nothing: %q %v", got, err)
	}
	// A session begun under a longer setting ends when it is made shorter.
	if _, _, err := db.SessionKey(ctx, hash("kept"), time.Nanosecond); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a session older than sessions now last is still found: %v", err)
	}
	// Only what is past its time is pruned.
	if _, err := db.PruneSessions(ctx, 1000); err != nil || !live("kept") {
		t.Fatalf("the prune took a live session: %v", err)
	}
	var left int
	conn := connect(t)
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM console_sessions WHERE api_key_id = $1::uuid`, k.ID).Scan(&left); err != nil || left != 1 {
		t.Fatalf("%d sessions left, want the live one: %v", left, err)
	}
	// Its key revoked, the session is no longer found.
	if err := keys.New(db).Revoke(ctx, k.Prefix); err != nil || live("kept") {
		t.Fatalf("a revoked key's session is still found: %v", err)
	}
}

// A key says which key made it, a sending domain holds only so many live
// keys, and a list is one domain's or everyone's, newest first.
func TestKeysAreCountedListedAndTraced(t *testing.T) {
	ctx, db := context.Background(), open(t)
	maker := key(t, db)
	domain := "d" + maker.Prefix[len(maker.Prefix)-8:] + ".example.com"
	ks := keys.New(db).WithMax(2)
	made := func(name string) (store.Key, error) {
		_, k, err := ks.Make(ctx, keys.Request{Name: name, Scope: store.ScopeSend, Domain: domain, By: maker.Prefix})
		return k, err
	}
	first, err := made("first")
	if err != nil || first.CreatedBy != maker.Prefix || first.Domain != domain {
		t.Fatalf("%+v %v", first, err)
	}
	if maker.CreatedBy != "" {
		t.Fatalf("a key nobody made says %q made it", maker.CreatedBy)
	}
	second, err := made("second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := made("third"); !errors.Is(err, keys.ErrLimit) {
		t.Fatalf("a third key of two: %v", err)
	}
	if err := ks.Revoke(ctx, first.Prefix); err != nil {
		t.Fatal(err)
	}
	third, err := made("third")
	if err != nil {
		t.Fatalf("a revoked key made no room: %v", err)
	}
	list, err := db.ListKeys(ctx, domain, 0)
	if err != nil || len(list) != 3 || list[0].ID != third.ID || list[1].ID != second.ID || list[2].ID != first.ID {
		t.Fatalf("the domain's keys, newest first: %+v %v", list, err)
	}
	if list, err = db.ListKeys(ctx, domain, 2); err != nil || len(list) != 2 || list[0].ID != third.ID {
		t.Fatalf("the newest two: %+v %v", list, err)
	}
	all, err := db.ListKeys(ctx, "", 0)
	if err != nil || len(all) < 4 {
		t.Fatalf("every key: %d %v", len(all), err)
	}
}

// connect opens a connection of the test's own, closed when it ends.
func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), os.Getenv("CAIRNPOST_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}
