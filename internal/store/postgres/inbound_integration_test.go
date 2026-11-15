//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// An email that came is kept once, within the room there is, read
// until its time is up, and deleted with what was owed for it.
func TestEmailsThatComeAreKeptWithinTheirRoomAndTime(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	conn, err := pgx.Connect(ctx, os.Getenv("CAIRNPOST_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	domain := strings.ReplaceAll(strings.ToLower(k.Prefix), "_", "-") + ".example"
	end, err := db.CreateEndpoint(ctx, store.Endpoint{URL: "https://app.example/hook", Domain: domain, Kinds: []string{store.KindReceived}, Secret: []byte("s")}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DeleteEndpoint(ctx, end.ID) })
	email := func(to, raw string, room int64) store.NewInbound {
		sum := sha256.Sum256([]byte(raw))
		return store.NewInbound{Inbound: store.Inbound{Domain: domain, Recipient: to + "@" + domain, Sender: "ana@example.org", Subject: "s"},
			Digest: sum[:], Raw: []byte(raw), Keep: time.Hour, Room: room}
	}
	owed := func() (n int) {
		t.Helper()
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE endpoint_id = $1::uuid`, end.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	first, again, err := db.Receive(ctx, email("a", "one "+k.Prefix, 0))
	if err != nil || again || first == "" || owed() != 1 {
		t.Fatalf("%s %v %v, %d owed", first, again, err, owed())
	}
	if id, again, err := db.Receive(ctx, email("a", "one "+k.Prefix, 0)); err != nil || !again || id != first || owed() != 1 {
		t.Fatalf("the same again: %s %v %v, %d owed", id, again, err, owed())
	}
	// The same email for another address is another email.
	second, again, err := db.Receive(ctx, email("b", "one "+k.Prefix, 0))
	if err != nil || again || second == first {
		t.Fatalf("for another address: %s %v %v", second, again, err)
	}
	// Room is what the emails kept hold in all: one byte short, it is refused.
	var held int64
	if err := conn.QueryRow(ctx, `SELECT coalesce(sum(bytes), 0) FROM inbound_emails WHERE expires_at > now()`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	raw := "three " + k.Prefix
	if _, _, err := db.Receive(ctx, email("c", raw, held+int64(len(raw))-1)); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("one byte past the room: %v", err)
	}
	third, _, err := db.Receive(ctx, email("c", raw, held+int64(len(raw))))
	if err != nil {
		t.Fatalf("just within the room: %v", err)
	}
	list, err := db.ListInbound(ctx, domain, "", 10)
	if err != nil || len(list) != 3 || list[0].ID != third || list[2].ID != first || list[0].Bytes != len(raw) {
		t.Fatalf("%+v %v", list, err)
	}
	if page, err := db.ListInbound(ctx, domain, second, 10); err != nil || len(page) != 1 || page[0].ID != first {
		t.Fatalf("before the second: %+v %v", page, err)
	}
	if none, err := db.ListInbound(ctx, "other.example", "", 10); err != nil || len(none) != 0 {
		t.Fatalf("another domain's: %+v %v", none, err)
	}
	if n, err := db.CountInbound(ctx, domain); err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	// Its time up and not yet pruned: not read, not counted, not told to
	// the endpoint, and no longer there to be the same as.
	if _, err := conn.Exec(ctx, `UPDATE inbound_emails SET expires_at = now() - interval '1 second' WHERE id = $1::uuid`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Inbound(ctx, first); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("read past its time: %v", err)
	}
	if _, err := db.InboundRaw(ctx, first); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("read as it came past its time: %v", err)
	}
	due, err := db.TakeDeliveries(ctx, 100, 100, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.Inbound != nil && d.Inbound.ID == first {
			t.Error("an email whose time is up was told to an endpoint")
		}
		if d.Endpoint.ID == end.ID && (d.Inbound == nil || d.Delivery.Kind != store.KindReceived || d.Inbound.Recipient == "" || d.Inbound.Subject != "") {
			t.Errorf("what is told: %+v", d)
		}
	}
	anew, again, err := db.Receive(ctx, email("a", "one "+k.Prefix, 0))
	if err != nil || again || anew == first {
		t.Fatalf("handed on after its time: %s %v %v", anew, again, err)
	}
	// Deleted, what was owed for it goes with it.
	before := owed()
	if err := db.DeleteInbound(ctx, second); err != nil || owed() != before-1 {
		t.Fatalf("deleted: %v, %d owed of %d", err, owed(), before)
	}
	if err := db.DeleteInbound(ctx, second); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
	for _, id := range []string{third, anew} {
		if err := db.DeleteInbound(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if owed() != 0 {
		t.Errorf("%d still owed", owed())
	}
}
