package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/session"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// A session is its key's until it is ended, its time is up, sessions are
// made shorter than it is old, or its key is revoked.
func TestASessionIsItsKeysUntilItEnds(t *testing.T) {
	ctx, db, now := context.Background(), memstore.New(), time.Now()
	db.Now = func() time.Time { return now }
	ks := keys.New(db)
	_, k, err := ks.Create(ctx, "ops", store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(db, time.Hour)
	token, err := s.Start(ctx, k)
	if err != nil || len(token) < 40 {
		t.Fatalf("%q %v", token, err)
	}
	other, _ := s.Start(ctx, k)
	if other == token {
		t.Fatal("two sessions share a token")
	}
	if got, err := s.Key(ctx, token); err != nil || got.ID != k.ID {
		t.Fatalf("%+v %v", got, err)
	}
	for _, wrong := range []string{"", "x", token + "x"} {
		if _, err := s.Key(ctx, wrong); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%q: %v", wrong, err)
		}
	}
	// The same sessions, read by a server whose sessions last less.
	now = now.Add(10 * time.Minute)
	if _, err := session.New(db, 5*time.Minute).Key(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a session older than sessions now last: %v", err)
	}
	if err := s.End(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Key(ctx, other); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an ended session: %v", err)
	}
	now = now.Add(time.Hour)
	if _, err := s.Key(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a session past its time: %v", err)
	}
	fresh, _ := s.Start(ctx, k)
	if err := ks.Revoke(ctx, k.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Key(ctx, fresh); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a revoked key's session: %v", err)
	}
}
