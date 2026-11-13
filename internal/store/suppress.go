package store

import (
	"context"
	"strings"
	"time"
)

// Why an address is suppressed.
const (
	ReasonBounce    = "bounce"
	ReasonComplaint = "complaint"
	ReasonManual    = "manual"
)

// Reasons is every reason.
var Reasons = []string{ReasonBounce, ReasonComplaint, ReasonManual}

// Suppression is an address CairnPost will not send to from a domain.
type Suppression struct {
	Domain  string
	Address string
	Reason  string
	// MessageID is the email whose event caused it, "" for none.
	MessageID string
	Note      string
	CreatedAt time.Time
}

// SuppressionFor is the suppression an event causes: a complaint, and a
// bounce that is for good and about the address. Nothing else suppresses.
//
// A receiving server can also refuse for good for a reason that is the
// sender's, not the recipient's: a failed signature, a policy, a blocklist.
// Those carry a status of class 5.7 ("security or policy"), and suppress
// nobody, or one fault in the sending domain's setup would shut out every
// person written to while it lasted.
func SuppressionFor(kind string, permanent bool, code string) (reason string, suppresses bool) {
	switch {
	case kind == EventBounced && permanent && !strings.HasPrefix(code, "5.7."):
		return ReasonBounce, true
	case kind == EventComplained:
		return ReasonComplaint, true
	}
	return "", false
}

// Fold is an address or a domain as a suppression keeps it: lowercase, so
// one mailbox is one row however it was typed.
func Fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// DomainOf is the domain of an address, folded.
func DomainOf(address string) string {
	return Fold(address[strings.LastIndex(address, "@")+1:])
}

// SuppressionKey is the row a send from sender to recipient is looked up
// under, and the row its bounce is kept under. Both are addresses in their
// wire form. Every path that reads or
// writes the list goes through it, so they cannot disagree.
func SuppressionKey(sender, recipient string) (domain, address string) {
	return DomainOf(sender), Fold(recipient)
}

// Suppressions keeps the addresses not to send to.
type Suppressions interface {
	// Suppress puts a suppression in force by hand, and says whether it
	// did: false means one was in force already, and stays as it was.
	Suppress(ctx context.Context, s Suppression) (bool, error)
	// Unsuppress lifts one; ErrNotFound for none in force.
	Unsuppress(ctx context.Context, domain, address string) error
	// UnsuppressAll lifts every suppression of a sending domain, or those
	// of one reason (reason "" for all), and answers how many.
	UnsuppressAll(ctx context.Context, domain, reason string) (int, error)
	// ListSuppressions answers up to limit suppressions in force, in order
	// of domain and address, of one domain or of all (""), starting after a
	// row.
	ListSuppressions(ctx context.Context, domain string, after *Suppression, limit int) ([]Suppression, error)
	// SuppressionsOf answers the suppressions in force of one address,
	// in order of domain: of one domain or of all ("").
	SuppressionsOf(ctx context.Context, domain, address string) ([]Suppression, error)
}
