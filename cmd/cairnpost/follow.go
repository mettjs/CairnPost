package main

import (
	"context"
	"fmt"
	"io"

	"github.com/mettjs/cairnpost/internal/store"
)

// follower shows events once each, in the order they are read.
type follower struct {
	events store.Events
	w      io.Writer
	domain string
	// after is the newest event of any sending domain at the last look,
	// "" while none was ever recorded; seen are the events the last
	// look read, which the next one reads again.
	after string
	seen  map[string]bool
}

// begin shows the last events and notes where to follow from.
func (f *follower) begin(ctx context.Context, last int) error {
	newest, err := f.events.EventsAfter(ctx, "", 0, "", 1)
	if err != nil || len(newest) == 0 {
		return err
	}
	var shown []store.Event
	if last > 0 {
		if shown, err = f.events.EventsAfter(ctx, "", 0, f.domain, last); err != nil {
			return err
		}
	}
	f.show(shown)
	// What a first look would read again is not new, shown or not.
	f.after = newest[0].ID
	recent, err := f.since(ctx)
	for _, e := range recent {
		f.seen[e.ID] = true
	}
	return err
}

// since reads the events recorded after the last look, and those a
// moment before it again.
func (f *follower) since(ctx context.Context) ([]store.Event, error) {
	var read []store.Event
	for from, back := f.after, tailBack; from != ""; back = 0 {
		page, err := f.events.EventsAfter(ctx, from, back, f.domain, tailPage)
		if err != nil {
			return nil, err
		}
		if read = append(read, page...); len(page) < tailPage {
			break
		}
		from = page[len(page)-1].ID
	}
	return read, nil
}

// look shows what was recorded since the last look. It follows the
// newest event of any sending domain, so that one domain's quiet does
// not make each look read further back.
func (f *follower) look(ctx context.Context) error {
	newest, err := f.events.EventsAfter(ctx, "", 0, "", 1)
	if err != nil || len(newest) == 0 {
		return err
	}
	var read []store.Event
	if f.after == "" {
		// The first ever recorded: the last of them is all of them,
		// unless more than a page came at once.
		if read, err = f.events.EventsAfter(ctx, "", 0, f.domain, tailPage); err != nil {
			return err
		}
	}
	more, err := f.since(ctx)
	if err != nil {
		return err
	}
	read = append(read, more...)
	f.show(read)
	f.after = newest[0].ID
	return nil
}

// show writes the events not shown before, one a line, and remembers
// these as the ones a next look may read again.
func (f *follower) show(events []store.Event) {
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		if seen[e.ID] = true; f.seen[e.ID] {
			continue
		}
		what := e.Kind
		if e.Kind == store.EventBounced && e.Permanent {
			what += " for good"
		}
		fmt.Fprintf(f.w, "%s  %-18s %-7s %s  from %s  email %s\n", e.At.UTC().Format("2006-01-02 15:04:05Z"), what, e.Code, e.Recipient, e.Sender, e.MessageID)
	}
	f.seen = seen
}
