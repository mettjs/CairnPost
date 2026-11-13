package store

import (
	"context"
	"time"
)

// States an event moves a message to, beyond the send's own.
const (
	StateDeferred   = "deferred"
	StateDelivered  = "delivered"
	StateBounced    = "bounced"
	StateComplained = "complained"
)

// Kinds of event.
const (
	EventDelivered  = "delivered"
	EventDeferred   = "deferred"
	EventBounced    = "bounced"
	EventComplained = "complained"
	EventFailed     = "failed"
)

// StateOf is the state each kind of event moves a message to. It is the one
// place a kind becomes a state, and it covers every kind.
var StateOf = map[string]string{
	EventDelivered: StateDelivered, EventDeferred: StateDeferred, EventBounced: StateBounced,
	EventComplained: StateComplained, EventFailed: StateFailed,
}

// Event is one thing that happened to an email after the upstream took it.
// It never holds the email's subject or body, nor any server's free text.
type Event struct {
	ID        string
	MessageID string
	// Driver and UpstreamEventID name the event as the upstream does.
	Driver          string
	UpstreamEventID string
	// UpstreamID is the upstream's id for the email the event is about.
	UpstreamID string
	Kind       string
	// Permanent says, for a bounce, that the address will never take mail.
	Permanent bool
	// Code is the receiving server's status, Reason the upstream's short
	// name for what happened; either may be "".
	Code, Reason string
	At           time.Time
}

// Recorded is what RecordEvent did with an event.
type Recorded int

const (
	// Kept: the event is new and was recorded.
	Kept Recorded = iota
	// Duplicate: the event was recorded before; nothing changed.
	Duplicate
	// NoMessage: no message has the upstream id the event names.
	NoMessage
)

// rank orders states by how much is known. Events arrive in any order, so
// a message never moves down: a late "deferred" does not undo a delivery.
var rank = map[string]int{
	StateSending: 0, StateUnknown: 0, StateSent: 1, StateDeferred: 2,
	StateDelivered: 3, StateBounced: 3, StateFailed: 3, StateComplained: 4,
}

// Moves reports whether an event of a kind moves a message out of a state.
// Up a rank, always. Between the outcomes of one rank (delivered, bounced,
// failed), the one that happened later stands: newer says whether this
// event is later than every event that put the message where it is.
func Moves(from, kind string, newer bool) bool {
	to, known := StateOf[kind]
	switch {
	case !known || to == from:
		return false
	case rank[to] > rank[from]:
		return true
	}
	return rank[to] == rank[from] && newer
}

// Events keeps what happened to emails.
type Events interface {
	// RecordEvent records an event and what follows from it, in one step:
	// either all of it is kept or none, so an event handed over twice has
	// its effects once. An event that says the email was never sent frees
	// the Idempotency-Key it was sent under, as a refusal at send does.
	RecordEvent(ctx context.Context, e Event) (Recorded, error)
	// EventsOf is a message's events, oldest first.
	EventsOf(ctx context.Context, messageID string) ([]Event, error)
	// Record is a message and its events as they stood at one moment;
	// ErrNotFound for an id no message has.
	Record(ctx context.Context, messageID string) (Message, []Event, error)
}
