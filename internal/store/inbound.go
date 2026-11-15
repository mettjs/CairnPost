package store

import (
	"context"
	"time"
)

// KindReceived is what an endpoint is owed for an email that came: it
// is no event of an email sent.
const KindReceived = "received"

// Inbound is an email that came for an address of a sending domain.
type Inbound struct {
	ID string
	// Domain is the domain of Recipient, the address it came for.
	Domain    string
	Recipient string
	// Sender is who it came from, as the sending server said: "" for a
	// bounce.
	Sender string
	// Subject and From are what its headers say, Files how many files
	// came with it, and Partial whether some of it could not be read:
	// as it was read when it came.
	Subject string
	From    string
	Files   int
	Partial bool
	// Bytes is how long it is as it came.
	Bytes      int
	ReceivedAt time.Time
	// ExpiresAt is when it stops being shown, and is deleted soon after.
	ExpiresAt time.Time
}

// NewInbound is an email to keep.
type NewInbound struct {
	Inbound
	// Digest is the SHA-256 of Raw, the email as it came.
	Digest []byte
	Raw    []byte
	// Keep is how long it is kept, and Room how many bytes the emails
	// kept may be in all, 0 for no limit.
	Keep time.Duration
	Room int64
}

// Inbounds keeps the emails that came.
type Inbounds interface {
	// Receive keeps an email and owes it to every enabled endpoint that
	// hears about its domain and about emails that come. The same email
	// for the same address, while it is kept, is kept once: again is
	// then true and the id the first one's. ErrLimit when the emails
	// kept have no room for it.
	Receive(ctx context.Context, n NewInbound) (id string, again bool, err error)
	// Inbound answers one that is kept; ErrNotFound for none, and for
	// one whose time is up.
	Inbound(ctx context.Context, id string) (Inbound, error)
	// InboundRaw answers one as it came; ErrNotFound as Inbound.
	InboundRaw(ctx context.Context, id string) ([]byte, error)
	// ListInbound answers the emails kept for a domain, or for all for
	// "", newest first, those before the id given, limit at most.
	ListInbound(ctx context.Context, domain, before string, limit int) ([]Inbound, error)
	// DeleteInbound deletes one, with what was owed for it; ErrNotFound
	// for none.
	DeleteInbound(ctx context.Context, id string) error
	// CountInbound answers how many are kept for a domain, or for all.
	CountInbound(ctx context.Context, domain string) (int, error)
}
