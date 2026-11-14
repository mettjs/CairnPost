package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// recorded is a store of events a test adds to while tail follows it.
type recorded struct {
	store.Events
	events chan []store.Event
	all    []store.Event
}

func (r *recorded) EventsAfter(_ context.Context, after, _ string, limit int) ([]store.Event, error) {
	select {
	case more := <-r.events:
		r.all = append(r.all, more...)
	default:
	}
	from := 0
	for i, e := range r.all {
		if e.ID == after {
			from = i + 1
		}
	}
	out := r.all[from:]
	if after == "" && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
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
	go func() { done <- tail(ctx, r, out, "", 2, 10*time.Millisecond, false) }()
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
	r.events <- []store.Event{event("4", store.EventComplained, false)}
	if line := next(); !strings.Contains(line, "complained") || !strings.Contains(line, "email m-4") {
		t.Fatalf("a new event: %q", line)
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
	if err := tail(context.Background(), r, out, "", 1, time.Second, true); err != nil || !strings.Contains(next(), "email m-4") {
		t.Fatalf("once: %v", err)
	}
}
