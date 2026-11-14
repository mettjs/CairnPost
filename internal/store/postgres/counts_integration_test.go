//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// counted sums a sending domain's counts over the last hours.
func counted(t *testing.T, s store.Counts, domain string, hours int) map[string]int {
	t.Helper()
	rows, now, err := s.SendCounts(context.Background(), domain, hours)
	if err != nil || now.IsZero() {
		t.Fatal(err)
	}
	sum := map[string]int{}
	for i, c := range rows {
		if c.Domain != domain || !c.Hour.Equal(c.Hour.Truncate(time.Hour)) || (i > 0 && c.Hour.Before(rows[i-1].Hour)) {
			t.Fatalf("row %d is not an hour of %s, in order: %+v", i, domain, c)
		}
		for state, n := range c.In {
			sum[state] += n
		}
	}
	return sum
}

// Every email is counted once, under the hour it was sent and the state it
// stands in, whatever wrote that state; and it stays counted when its
// record is gone.
func TestEmailsAreCountedByStateAndStayCountedWhenTheirRecordGoes(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := "counts-" + k.ID + ".example.com"
	up := func(n string) string { return "<" + n + "-" + k.ID + ">" }
	s := start(k, "", "")
	s.Sender = "app@" + domain
	if c, err := db.Start(ctx, s); err != nil || c.Outcome != store.Started { // left sending
		t.Fatalf("%v %v", c.Outcome, err)
	}
	sentFrom(t, db, k, "app@"+domain, up("sent"))
	sentFrom(t, db, k, "app@"+domain, up("delivered"))
	sentFrom(t, db, k, "app@"+domain, up("bounced"))
	arrived := store.Event{Driver: "test", UpstreamEventID: "evt-" + up("delivered"), UpstreamID: up("delivered"),
		Kind: store.EventDelivered, At: time.Now()}
	for range 2 { // handed over twice, counted once
		if _, err := db.RecordEvent(ctx, arrived); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.RecordEvent(ctx, bounced(up("bounced"), "5.1.1")); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{store.StateSending: 1, store.StateSent: 1, store.StateDelivered: 1, store.StateBounced: 1}
	if got := counted(t, db, domain, 2); !maps.Equal(got, want) {
		t.Fatalf("counted %v, want %v", got, want)
	}

	// The counts are what the records say, state by state.
	onRecord, conn := map[string]int{}, connect(t)
	rows, err := conn.Query(ctx, `SELECT state, count(*) FROM messages WHERE sender_domain = $1 GROUP BY state`, domain)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var (
			state string
			n     int
		)
		if err := rows.Scan(&state, &n); err != nil {
			t.Fatal(err)
		}
		onRecord[state] = n
	}
	if !maps.Equal(onRecord, want) {
		t.Fatalf("on record %v, counted %v", onRecord, want)
	}

	// A send left unanswered moves to unknown in its count as well.
	exec(t, `UPDATE messages SET created_at = created_at - interval '3 hours' WHERE sender_domain = $1 AND state = 'sending'`, domain)
	for {
		n, err := db.MarkUnanswered(ctx, time.Hour, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n < 1000 {
			break
		}
	}
	want = map[string]int{store.StateUnknown: 1, store.StateSent: 1, store.StateDelivered: 1, store.StateBounced: 1}
	if got := counted(t, db, domain, 6); !maps.Equal(got, want) {
		t.Fatalf("after the sweep: counted %v, want %v", got, want)
	}
	// It is counted under the hour it was sent, three hours back.
	if got := counted(t, db, domain, 2); got[store.StateUnknown] != 0 || got[store.StateSent] != 1 {
		t.Fatalf("the last two hours: %v", got)
	}
	// Another domain's key sees none of it, and every domain's includes it.
	if got := counted(t, db, "nobody-"+domain, 6); len(got) != 0 {
		t.Fatalf("another domain: %v", got)
	}
	if all, _, err := db.SendCounts(ctx, "", 6); err != nil || len(all) < 2 {
		t.Fatalf("every domain: %d rows, %v", len(all), err)
	}

	// The records go; the counts stay.
	exec(t, `DELETE FROM idempotency_keys WHERE api_key_id = $1::uuid`, k.ID)
	exec(t, `DELETE FROM messages WHERE sender_domain = $1`, domain)
	if got := counted(t, db, domain, 6); !maps.Equal(got, want) {
		t.Fatalf("with the records gone: counted %v, want %v", got, want)
	}

	// A message whose hour's count is gone is not counted again when its
	// state moves: no row is made for one email.
	late := sentFrom(t, db, k, "app@late-"+domain, up("late"))
	exec(t, `DELETE FROM send_counts WHERE domain = $1`, "late-"+domain)
	if _, err := db.RecordEvent(ctx, bounced(up("late"), "5.1.1")); err != nil {
		t.Fatal(err)
	}
	if m, err := db.Message(ctx, late); err != nil || m.State != store.StateBounced {
		t.Fatalf("%q %v", m.State, err)
	}
	if got := counted(t, db, "late-"+domain, 6); len(got) != 0 {
		t.Fatalf("a count was made anew for one late event: %v", got)
	}

	// Until their own time is up.
	exec(t, `UPDATE send_counts SET hour = hour - interval '40 days' WHERE domain = $1`, domain)
	if _, err := db.PruneCounts(ctx, 62*24*time.Hour, 1000); err != nil {
		t.Fatal(err)
	}
	if got := counted(t, db, domain, 24*50); !maps.Equal(got, want) {
		t.Fatalf("forty days on: %v", got)
	}
	exec(t, `UPDATE send_counts SET hour = hour - interval '30 days' WHERE domain = $1`, domain)
	for {
		n, err := db.PruneCounts(ctx, 62*24*time.Hour, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n < 1000 {
			break
		}
	}
	if got := counted(t, db, domain, 24*100); len(got) != 0 {
		t.Fatalf("past their time: %v", fmt.Sprint(got))
	}
}

// The table's columns are the states a message can be in, name for name:
// a state with no column would drop out of the counts and nothing would
// say so.
func TestEveryStateHasItsColumnInTheCounts(t *testing.T) {
	open(t)
	rows, err := connect(t).Query(context.Background(), `SELECT column_name::text FROM information_schema.columns
		WHERE table_name = 'send_counts' AND column_name NOT IN ('hour', 'domain')`)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	states := map[string]bool{}
	for _, state := range store.States {
		states[state] = true
	}
	if !maps.Equal(columns, states) {
		t.Fatalf("columns %v, states %v", columns, states)
	}
}
