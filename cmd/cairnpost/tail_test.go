package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// recorded is a store of events a test adds to while tail follows it.
// Ids are in order as text; looking back answers two more.
type recorded struct {
	store.Events
	events chan []store.Event
	all    []store.Event
	fail   error
	// failFrom is the read from which every one fails; 0 for none.
	failFrom, reads int
}

func (r *recorded) EventsAfter(_ context.Context, after string, back time.Duration, _ string, limit int) ([]store.Event, error) {
	if r.reads++; r.failFrom > 0 && r.reads >= r.failFrom {
		return nil, errors.New("the database is away")
	}
	if r.fail != nil {
		return nil, r.fail
	}
	select {
	case more := <-r.events:
		r.all = append(r.all, more...)
		slices.SortFunc(r.all, func(a, b store.Event) int { return strings.Compare(a.ID, b.ID) })
	default:
	}
	from := 0
	for i, e := range r.all {
		if e.ID <= after {
			from = i + 1
		}
	}
	if back > 0 {
		from = max(from-2, 0)
	}
	out := r.all[from:]
	switch {
	case after == "" && len(out) > limit:
		out = out[len(out)-limit:]
	case len(out) > limit:
		out = out[:limit]
	}
	return slices.Clone(out), nil
}

type lines struct{ got chan string }

func (l lines) Write(p []byte) (int, error) { l.got <- string(p); return len(p), nil }

// Tail shows the last events, then each new one once, and stops when
// it is told to.
func TestTailShowsTheLastEventsAndThenEachNewOne(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	event := func(id, kind string, permanent bool) store.Event {
		return store.Event{ID: id, MessageID: "m-" + id, Kind: kind, Permanent: permanent, Code: "5.1.1", At: at,
			Sender: "app@example.com", Recipient: "ana@example.org"}
	}
	r := &recorded{events: make(chan []store.Event, 1), all: []store.Event{event("1", store.EventDelivered, false),
		event("2", store.EventDelivered, false), event("3", store.EventBounced, true)}}
	out := lines{make(chan string, 10)}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tail(ctx, r, out, io.Discard, "", 2, 10*time.Millisecond, false) }()
	next := func() string {
		t.Helper()
		select {
		case line := <-out.got:
			return line
		case <-time.After(5 * time.Second):
			t.Fatal("no line came")
			return ""
		}
	}
	if first, second := next(), next(); !strings.Contains(first, "email m-2") ||
		second != "2030-01-02 03:04:05Z  bounced for good   5.1.1   ana@example.org  from app@example.com  email m-3\n" {
		t.Fatalf("the last two: %q %q", first, second)
	}
	r.events <- []store.Event{event("5", store.EventComplained, false)}
	if line := next(); !strings.Contains(line, "complained") || !strings.Contains(line, "email m-5") {
		t.Fatalf("a new event: %q", line)
	}
	// One recorded before the newest, that comes into view after it, is
	// shown all the same, and once.
	r.events <- []store.Event{event("4", store.EventDelivered, false), event("6", store.EventDelivered, false)}
	if late, new := next(), next(); !strings.Contains(late, "email m-4") || !strings.Contains(new, "email m-6") {
		t.Fatalf("a late event: %q %q", late, new)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("stopped: %v", err)
	}
	select {
	case line := <-out.got:
		t.Fatalf("an event was shown twice: %q", line)
	default:
	}
	// Asked for the last ones alone, it stops by itself.
	if err := tail(context.Background(), r, out, io.Discard, "", 1, time.Second, true); err != nil || !strings.Contains(next(), "email m-6") {
		t.Fatalf("once: %v", err)
	}
}

// A store that stays away ends tail with the reason, each failed look
// said meanwhile; and more than a page of new events are all shown.
func TestTailGivesUpOnAStoreThatStaysAwayAndReadsEveryPage(t *testing.T) {
	said := lines{make(chan string, 2*tailTries)}
	gone := &recorded{all: []store.Event{{ID: "1"}}, failFrom: 3}
	err := tail(context.Background(), gone, io.Discard, said, "", 0, time.Millisecond, false)
	if err == nil || !strings.Contains(err.Error(), "the database is away") || len(said.got) != tailTries-1 {
		t.Fatalf("%v, said %d times", err, len(said.got))
	}
	if line := <-said.got; !strings.Contains(line, "the database is away") {
		t.Fatalf("the reason was not said: %q", line)
	}
	away := &recorded{all: []store.Event{{ID: "1"}}}
	f := &follower{events: away, w: io.Discard}
	if err := f.begin(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	away.fail = errors.New("the database is away")
	if err := f.look(context.Background()); err == nil {
		t.Fatal("a failed look was not said")
	}
	away.fail = nil
	var many []store.Event
	for i := range 2*tailPage + 5 {
		many = append(many, store.Event{ID: fmt.Sprintf("2-%04d", i)})
	}
	away.all = append(away.all, many...)
	out := &strings.Builder{}
	f.w = out
	if err := f.look(context.Background()); err != nil || strings.Count(out.String(), "\n") != len(many) {
		t.Fatalf("%d lines of %d, %v", strings.Count(out.String(), "\n"), len(many), err)
	}
	if err := f.look(context.Background()); err != nil || strings.Count(out.String(), "\n") != len(many) {
		t.Fatalf("a second look showed some again: %v", err)
	}
}
