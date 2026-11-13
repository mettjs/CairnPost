package driver

import (
	"context"
	"time"
)

// Kinds of event an upstream reports. This is the whole set: a driver maps
// whatever its upstream says onto one of these, or does not report it.
const (
	Delivered  = "delivered"
	Deferred   = "deferred"
	Bounced    = "bounced"
	Complained = "complained"
	// Failed is an email the upstream took and then could not or would not
	// send: its own error, or its policy.
	Failed = "failed"
)

// Kinds is every kind.
var Kinds = []string{Delivered, Deferred, Bounced, Complained, Failed}

// Event is one thing an upstream says happened to an email it took. It
// holds nothing of the email's subject or body, and no server's free text,
// which can quote either.
type Event struct {
	// ID is the upstream's own id for the event; the same event handed
	// over twice has the same id.
	ID string
	// UpstreamID is the id Send answered for the email.
	UpstreamID string
	Kind       string
	// Permanent says, for a bounce, that the address will never take mail.
	Permanent bool
	// Code is the receiving server's status ("5.1.1"), and Reason the
	// upstream's own short name for what happened ("permanent_failure"):
	// both from closed vocabularies, never a sentence.
	Code, Reason string
	// At is when it happened; zero when the upstream did not say.
	At time.Time
	// Sender and Recipient are the email's addresses when the upstream
	// names them, bare and as written on the wire, "" otherwise. They are
	// for suppressing an address whose email has no record; never logged.
	Sender, Recipient string
}

// Received is one item taken from an upstream's stream of events.
type Received struct {
	// Lease is what settles the item: acknowledged, or handed back.
	Lease string
	// Attempts is how many times the item was handed over before this one.
	Attempts int
	// Event is the item as read. Readable is false for one that is no
	// event about an email, or lacks what names it; Kind is still set when
	// that much could be told.
	Event    Event
	Readable bool
}

// Source is an upstream's stream of events. An item taken and not settled
// comes back after its lease, so a reader that dies mid-way loses nothing;
// but an upstream may give an item only so many times before dropping it,
// so an item is not left to lapse lightly.
type Source interface {
	// Take answers up to max waiting items, or none, each ours for lease.
	Take(ctx context.Context, max int, lease time.Duration) ([]Received, error)
	// Settle acknowledges the items that are dealt with, for good, and
	// hands back the ones to be given again after a delay.
	Settle(ctx context.Context, done, again []string, delay time.Duration) error
}
