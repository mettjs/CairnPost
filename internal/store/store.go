// Package store declares what CairnPost keeps and the repositories that keep
// it. store/postgres is the real one and the only code that issues SQL;
// store/memstore stands in for it in tests.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is a row that is not there.
var ErrNotFound = errors.New("store: not found")

// Scopes of an API key.
const (
	ScopeSend  = "send"
	ScopeAdmin = "admin"
)

// Key is an API key as it is kept: never the key itself.
type Key struct {
	ID        string
	Name      string
	Prefix    string
	Hash      []byte
	Scope     string
	Domain    string // "" for any sending domain
	CreatedAt time.Time
	RevokedAt *time.Time
}

// Keys keeps API keys.
type Keys interface {
	CreateKey(ctx context.Context, k Key) (Key, error)
	// KeyByPrefix answers ErrNotFound for a prefix no key has.
	KeyByPrefix(ctx context.Context, prefix string) (Key, error)
	ListKeys(ctx context.Context) ([]Key, error)
	// RevokeKey answers ErrNotFound for a prefix no live key has.
	RevokeKey(ctx context.Context, prefix string) error
}

// States of a message.
const (
	StateSending = "sending"
	StateSent    = "sent"
	StateFailed  = "failed"
	StateUnknown = "unknown"
)

// Message is one email to one recipient, as it is kept: who, to whom, by
// which upstream and what became of it. Never its subject or its body.
type Message struct {
	ID         string
	KeyID      string
	Sender     string
	Recipient  string
	Driver     string
	UpstreamID string
	State      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Outcome is what Start found under an Idempotency-Key.
type Outcome int

const (
	// Started: this request is the one that sends.
	Started Outcome = iota
	// Replay: the same request was sent before; its message is answered.
	Replay
	// InFlight: the same request is being sent now, or its answer was lost
	// too recently to send again.
	InFlight
	// Mismatch: the key was used before with a different request, which was
	// sent. Only a finished send can mismatch: until then the key is in
	// flight, whatever the request.
	Mismatch
	// Suppressed: the recipient is on the sending domain's suppression
	// list, so nothing is sent. Only a send that would start is refused: a
	// request already sent is still a Replay. If the key's earlier try was
	// left in doubt, the claim carries that try's message id.
	Suppressed
)

// Claim is Start's answer.
type Claim struct {
	Outcome Outcome
	// ID is the message to send (Started), the one already sent (Replay),
	// or an earlier try left in doubt (Suppressed), "" when there is none.
	ID string
	// RetryAfter is, for InFlight, how long until a retry is let through.
	RetryAfter time.Duration
}

// Start is a send about to be made.
type Start struct {
	KeyID     string
	Sender    string
	Recipient string
	Driver    string
	// IdemKey is the caller's Idempotency-Key, "" for none.
	IdemKey     string
	Fingerprint []byte
	// TTL is how long a key is remembered; InDoubt how long a try with no
	// answer blocks the next one.
	TTL, InDoubt time.Duration
}

// Mail keeps messages and the idempotency keys they were sent under.
type Mail interface {
	// Start records a message as sending and claims its Idempotency-Key, in
	// one step, so that of two racing requests exactly one is Started.
	Start(ctx context.Context, s Start) (Claim, error)
	// Sent marks a message taken by the upstream and its key done.
	Sent(ctx context.Context, id, upstreamID string) error
	// Lost marks a message whose upstream answer never came: its fate is
	// unknown. Its key stays in flight until the in-doubt window passes.
	Lost(ctx context.Context, id string) error
	// Failed marks a message the upstream certainly did not take, and frees
	// its key so the next try sends at once.
	Failed(ctx context.Context, id string) error
	// Message answers ErrNotFound for an id no message has.
	Message(ctx context.Context, id string) (Message, error)
}

// Store is everything, as the composition root holds it.
type Store interface {
	Keys
	Mail
	Events
	Suppressions
	// Ping reports whether the store can be reached.
	Ping(ctx context.Context) error
}
