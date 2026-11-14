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
// It never holds the email's subject or body. A server's free text is in
// Detail alone, on its way to being kept apart.
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
	// Detail is the receiving server's own sentence, "" for none. It can
	// quote the email, so RecordEvent keeps it apart from the event, under
	// the rule for what emails say, and nothing but Details answers it.
	// KeepDetail is how long after its email was sent it is kept: a
	// sentence that comes later than that is not kept at all.
	Detail     string
	KeepDetail time.Duration
	// Suppressed says that, with this event, the address was on its
	// sending domain's suppression list. It is set by RecordEvent and
	// answered with a delivery to be tried (Webhooks.TakeDeliveries).
	Suppressed bool
	// Sender and Recipient are, on an event handed to RecordEvent, the
	// email's addresses as the event gives them, "" when it does not:
	// Recipient says which address of the email it is about, and both
	// serve, when no message has the event's upstream id, to suppress an
	// address that bounced for good all the same. They are never kept
	// with the event. On an event read back they are the addresses of
	// the email it was recorded for.
	Sender, Recipient string
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
	// the Idempotency-Key it was sent under, as a refusal at send does. A
	// bounce for good, or a complaint, suppresses the address, also when
	// the event answers NoMessage and names the addresses itself; such an
	// event never brings back a suppression that was lifted.
	RecordEvent(ctx context.Context, e Event) (Recorded, error)
	// EventsOf is an email's events, oldest first, of every address of
	// it: each says which address it is about (Recipient).
	EventsOf(ctx context.Context, messageID string) ([]Event, error)
	// EventsAfter answers events in the order they were recorded, oldest
	// first, each with its email's addresses (Sender, Recipient): those
	// recorded after the event after names, or, for "", the last limit
	// of them. Of one sending domain, or of all ("").
	//
	// An event can come into view a moment after one recorded later
	// than it. Whoever follows the events therefore asks again for the
	// ones up to back before after as well, and passes over those it
	// has seen.
	EventsAfter(ctx context.Context, after string, back time.Duration, domain string, limit int) ([]Event, error)
	// Record is an email and its events as they stood at one moment;
	// ErrNotFound for an id no message has.
	Record(ctx context.Context, messageID string) (Message, []Event, error)
	// Details answers the receiving servers' sentences kept for a
	// message's events, by event id, while they are kept and the message
	// was sent less than keep ago.
	Details(ctx context.Context, messageID string, keep time.Duration) (map[string]string, error)
}
