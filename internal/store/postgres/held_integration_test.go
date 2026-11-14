//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
)

// taken finds one email among those a look took, and puts the others
// back: the database outlives a run, and holds other tests' emails.
func taken(t *testing.T, db *postgres.DB, id string) (store.Held, bool) {
	t.Helper()
	due, err := db.TakeDue(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var found store.Held
	for _, h := range due {
		if h.ID == id {
			found = h
		} else if err := db.Postpone(context.Background(), h.ID, 0); err != nil {
			t.Fatal(err)
		}
	}
	return found, found.ID != ""
}

// An email asked for ahead of time waits apart from the messages, is
// taken by one sender when its time comes, begun under the id it waited
// with, begun again on the same rows after a try that failed, and is
// gone from those that wait once it is sent.
func TestAnEmailWaitsAndIsSentUnderItsId(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	k := key(t, db)
	domain := k.ID + ".example.com"
	to := []store.Addressee{{Address: "ana-" + k.ID + "@example.org", Kind: store.KindTo}, {Address: "cy-" + k.ID + "@example.org", Kind: store.KindCc}}
	ask := store.Start{KeyID: k.ID, Sender: "app@" + domain, Recipients: to, Driver: "test", IdemKey: "later", Fingerprint: []byte("one"),
		TTL: time.Hour, InDoubt: time.Minute,
		Hold: &store.Hold{SendAt: time.Now().Add(-time.Second), Words: []byte(`{"subject":"s"}`),
			Files: []store.HeldFile{{Filename: "a.txt", Type: "text/plain", Content: []byte("bytes")}}}}
	claim, err := db.Start(ctx, ask)
	if err != nil || claim.Outcome != store.Started || claim.ID == "" {
		t.Fatalf("asked for: %+v %v", claim, err)
	}
	id := claim.ID
	// Asked for and done with: the same request again is that email.
	if again, err := db.Start(ctx, ask); err != nil || again.Outcome != store.Replay || again.ID != id {
		t.Fatalf("asked again: %+v %v", again, err)
	}
	other := ask
	other.Fingerprint = []byte("two")
	if c, err := db.Start(ctx, other); err != nil || c.Outcome != store.Mismatch {
		t.Fatalf("another request under the key: %+v %v", c, err)
	}
	full := ask
	full.IdemKey, full.Hold = "", &store.Hold{SendAt: ask.Hold.SendAt, Words: []byte("w"), Room: 1}
	if c, err := db.Start(ctx, full); err != nil || c.Outcome != store.Full {
		t.Fatalf("with no room: %+v %v", c, err)
	}
	if _, err := db.Message(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an email that waits is among the messages: %v", err)
	}
	h, err := db.Held(ctx, id)
	if err != nil || h.State != store.StateScheduled || h.KeyID != k.ID || !slices.Equal(h.Recipients, to) || h.Tries != 0 {
		t.Fatalf("as it waits: %+v %v", h, err)
	}
	words, files, err := db.HeldWords(ctx, id)
	if err != nil || string(words) != `{"subject":"s"}` || len(files) != 1 || string(files[0].Content) != "bytes" || files[0].Type != "text/plain" {
		t.Fatalf("what it says: %s %+v %v", words, files, err)
	}
	if n, err := db.Waiting(ctx, domain); n != 1 || err != nil {
		t.Fatalf("%d wait: %v", n, err)
	}
	// Moved ahead, it is not due; moved back, it is.
	if err := db.Reschedule(ctx, id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if list, err := db.ListHeld(ctx, domain, time.Now(), 10); err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("listed: %+v %v", list, err)
	}
	if _, ok := taken(t, db, id); ok {
		t.Fatal("taken before its time")
	}
	if err := db.Reschedule(ctx, id, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	h, ok := taken(t, db, id)
	if !ok || h.Tries != 1 {
		t.Fatalf("at its time: %+v %v", h, ok)
	}
	// Taken, it is the sender's: nobody else takes or cancels it.
	if _, ok := taken(t, db, id); ok {
		t.Fatal("taken twice")
	}
	if err := db.Cancel(ctx, id); !errors.Is(err, store.ErrTooLate) {
		t.Fatalf("cancelled while it is sent: %v", err)
	}
	begin := store.Start{ID: id, KeyID: k.ID, Sender: h.Sender, Recipients: h.Recipients, Driver: "test"}
	if c, err := db.Start(ctx, begin); err != nil || c.Outcome != store.Started || c.ID != id || len(c.Recipients) != 2 {
		t.Fatalf("begun: %+v %v", c, err)
	}
	// The provider does not take it: it waits again, and its key stays.
	if err := db.Unsent(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := db.Postpone(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if again, err := db.Start(ctx, ask); err != nil || again.Outcome != store.Replay {
		t.Fatalf("asked again between tries: %+v %v", again, err)
	}
	// An address put on the list between the tries is left out of the next.
	if _, err := db.Suppress(ctx, store.Suppression{Domain: domain, Address: to[1].Address, Reason: store.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	if h, ok = taken(t, db, id); !ok || h.Tries != 2 {
		t.Fatalf("the second try: %+v %v", h, ok)
	}
	c, err := db.Start(ctx, begin)
	if err != nil || c.Outcome != store.Started || len(c.Recipients) != 1 || len(c.Dropped) != 1 {
		t.Fatalf("begun again: %+v %v", c, err)
	}
	if err := db.Sent(ctx, id, "<up>"); err != nil {
		t.Fatal(err)
	}
	if err := db.Release(ctx, id); err != nil {
		t.Fatal(err)
	}
	m, err := db.Message(ctx, id)
	if err != nil || m.State != store.StateSent || len(m.Recipients) != 2 || m.Recipients[1].State != store.StateFailed || len(m.LeftOut) != 1 {
		t.Fatalf("the message: %+v %v", m, err)
	}
	if _, err := db.Held(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sent, it still waits: %v", err)
	}
	if words, files, _ := db.HeldWords(ctx, id); words != nil || files != nil {
		t.Fatal("sent, what it said is still held")
	}
	// One email, sent once: the hour's count has it once.
	all, err := db.Messages(ctx, store.MessageQuery{Domain: domain, Limit: 10})
	if err != nil || len(all) != 1 {
		t.Fatalf("%d emails on record: %v", len(all), err)
	}
}

// A cancelled email keeps that it was asked for and nothing it said; a
// key revoked while its email waits cancels it; and what a stopped
// server had taken waits again.
func TestAWaitingEmailIsCancelledAndRecovered(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	k := key(t, db)
	ask := func(key store.Key) string {
		t.Helper()
		c, err := db.Start(ctx, store.Start{KeyID: key.ID, Sender: "app@" + k.ID + ".example.com",
			Recipients: store.To("ana-" + k.ID + "@example.org"), Driver: "test",
			Hold: &store.Hold{SendAt: time.Now().Add(-time.Second), Words: []byte("WORDS"),
				Files: []store.HeldFile{{Filename: "a", Type: "text/plain", Content: []byte("x")}}}})
		if err != nil || c.Outcome != store.Started {
			t.Fatalf("%+v %v", c, err)
		}
		return c.ID
	}
	id := ask(k)
	if err := db.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	h, err := db.Held(ctx, id)
	words, files, _ := db.HeldWords(ctx, id)
	if err != nil || h.State != store.StateCancelled || h.Reason != "" || words != nil || files != nil {
		t.Fatalf("cancelled: %+v %v, words %q", h, err, words)
	}
	if err := db.Cancel(ctx, id); !errors.Is(err, store.ErrTooLate) {
		t.Fatalf("cancelled twice: %v", err)
	}
	if err := db.Reschedule(ctx, id, time.Now().Add(time.Hour)); !errors.Is(err, store.ErrTooLate) {
		t.Fatalf("a cancelled email moved: %v", err)
	}
	if err := db.Cancel(ctx, "01900000-0000-7000-8000-000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an email there is not: %v", err)
	}
	if _, ok := taken(t, db, id); ok {
		t.Fatal("a cancelled email was taken")
	}
	horizon, err := db.Horizon(ctx, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := db.PruneCancelled(ctx, horizon, 1000); n < 1 || err != nil {
		t.Fatalf("%d pruned: %v", n, err)
	}
	if _, err := db.Held(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pruned, it is still there: %v", err)
	}

	_, other, err := keys.New(db).Create(ctx, t.Name()+" other", store.ScopeSend, "")
	if err != nil {
		t.Fatal(err)
	}
	revoked := ask(other)
	if err := db.RevokeKey(ctx, other.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, ok := taken(t, db, revoked); ok {
		t.Fatal("an email of a revoked key was taken")
	}
	h, err = db.Held(ctx, revoked)
	words, _, _ = db.HeldWords(ctx, revoked)
	if err != nil || h.State != store.StateCancelled || h.Reason != store.HeldKeyRevoked || words != nil {
		t.Fatalf("of a revoked key: %+v %v", h, err)
	}

	left := ask(k)
	if _, ok := taken(t, db, left); !ok {
		t.Fatal("not taken")
	}
	if n, err := db.Recover(ctx, 0, 1000); n < 1 || err != nil {
		t.Fatalf("%d recovered: %v", n, err)
	}
	if h, ok := taken(t, db, left); !ok || h.Tries != 2 {
		t.Fatalf("after recovery: %+v %v", h, ok)
	}
	if err := db.Drop(ctx, left, store.HeldSuppressed); err != nil {
		t.Fatal(err)
	}
	if h, err := db.Held(ctx, left); err != nil || h.State != store.StateCancelled || h.Reason != store.HeldSuppressed {
		t.Fatalf("dropped: %+v %v", h, err)
	}
}
