package memstore_test

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// The in-memory store counts as the database does: once per email, under
// the hour it was sent and the state it stands in, records or no records.
func TestEmailsAreCountedByStateAndStayCounted(t *testing.T) {
	ctx, s := context.Background(), memstore.New()
	now := time.Date(2030, 3, 1, 10, 20, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	send := func(from, up string) string {
		t.Helper()
		c, err := s.Start(ctx, store.Start{KeyID: "k", Sender: from, Recipient: "to-" + up + "@example.org", Driver: "test",
			TTL: time.Hour, InDoubt: time.Minute})
		if err != nil || c.Outcome != store.Started {
			t.Fatalf("%v %v", c.Outcome, err)
		}
		if up != "" {
			if err := s.Sent(ctx, c.ID, up); err != nil {
				t.Fatal(err)
			}
		}
		return c.ID
	}
	sum := func(domain string, hours int) map[string]int {
		t.Helper()
		rows, at, err := s.SendCounts(ctx, domain, hours)
		if err != nil || !at.Equal(now) {
			t.Fatalf("%v at %v", err, at)
		}
		out := map[string]int{}
		for _, c := range rows {
			for state, n := range c.In {
				out[state] += n
			}
		}
		return out
	}
	send("app@example.com", "")
	send("app@example.com", "<a>")
	send("App@Example.COM", "<b>")
	send("app@other.example.com", "<c>")
	for range 2 {
		if _, err := s.RecordEvent(ctx, store.Event{Driver: "test", UpstreamEventID: "e1", UpstreamID: "<b>",
			Kind: store.EventDelivered, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]int{store.StateSending: 1, store.StateSent: 1, store.StateDelivered: 1}
	if got := sum("example.com", 1); !maps.Equal(got, want) {
		t.Fatalf("counted %v, want %v", got, want)
	}
	if got := sum("", 1); got[store.StateSent] != 2 {
		t.Fatalf("every domain: %v", got)
	}

	// Three hours on, the send nobody answered is unknown, in the hour it
	// was sent; that hour is outside the last two.
	now = now.Add(3 * time.Hour)
	if n, err := s.MarkUnanswered(ctx, time.Hour, 10); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	want = map[string]int{store.StateUnknown: 1, store.StateSent: 1, store.StateDelivered: 1}
	if got := sum("example.com", 4); !maps.Equal(got, want) {
		t.Fatalf("after the sweep: %v, want %v", got, want)
	}
	if got := sum("example.com", 3); len(got) != 0 {
		t.Fatalf("the last three hours: %v", got)
	}

	// Records are pruned; counts are not, until their own time is up.
	now = now.Add(40 * 24 * time.Hour)
	if _, err := s.PruneKeys(ctx, time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneMessages(ctx, now, 10); err != nil || n != 4 {
		t.Fatalf("pruned %d records: %v", n, err)
	}
	if n, err := s.PruneCounts(ctx, 62*24*time.Hour, 10); err != nil || n != 0 {
		t.Fatalf("pruned %d counts early: %v", n, err)
	}
	if got := sum("example.com", 24*50); !maps.Equal(got, want) {
		t.Fatalf("with the records gone: %v, want %v", got, want)
	}
	now = now.Add(30 * 24 * time.Hour)
	if n, err := s.PruneCounts(ctx, 62*24*time.Hour, 10); err != nil || n != 2 {
		t.Fatalf("pruned %d counts: %v", n, err)
	}
	if got := sum("", 24*100); len(got) != 0 {
		t.Fatalf("past their time: %v", got)
	}
}
