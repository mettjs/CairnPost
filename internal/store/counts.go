package store

import (
	"context"
	"time"
)

// Counted is how many of the emails sent in one hour, from one sending
// domain, stand in each state now. An email is counted under the hour it
// was sent, whatever happened to it later and whenever.
type Counted struct {
	// Hour is the start of the hour, in UTC.
	Hour   time.Time
	Domain string
	// In is the number of emails by state; a state with none is absent.
	In map[string]int
}

// Counts keeps how many emails were sent and what became of them, apart
// from the emails' own records: a record that is pruned stays counted.
type Counts interface {
	// SendCounts answers the counts of the last hours hours, the one
	// under way included, oldest first: those of one sending domain, or
	// of every domain for "". With them it answers the store's own time,
	// which the hours are measured from.
	SendCounts(ctx context.Context, domain string, hours int) ([]Counted, time.Time, error)
}
