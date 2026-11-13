package store

import (
	"context"
	"time"
)

// SendingDomain is a domain mail is sent from, or can be: one a live key
// is bound to, or one a message on record was sent from.
type SendingDomain struct {
	Name string
	// Keys is how many keys that are not revoked are bound to it.
	Keys int
	// LastSent is when the newest message on record was sent from it;
	// nil for none.
	LastSent *time.Time
}

// Domains answers which domains mail is sent from.
type Domains interface {
	// SendingDomains lists them in order of name, at most limit.
	SendingDomains(ctx context.Context, limit int) ([]SendingDomain, error)
	// SendingDomain answers one by its folded name; ErrNotFound for a
	// name no key is bound to and nothing was sent from.
	SendingDomain(ctx context.Context, name string) (SendingDomain, error)
}
