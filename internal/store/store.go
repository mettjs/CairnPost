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

// ErrLimit is one more than there may be.
var ErrLimit = errors.New("store: no more may be made")

// Scopes of an API key.
const (
	ScopeSend  = "send"
	ScopeAdmin = "admin"
)

// Key is an API key as it is kept: never the key itself.
type Key struct {
	ID     string
	Name   string
	Prefix string
	Hash   []byte
	Scope  string
	Domain string // "" for any sending domain
	// CreatedBy is the prefix of the key that made this one, "" for a key
	// made on the command line.
	CreatedBy string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// Keys keeps API keys.
type Keys interface {
	// CreateKey keeps a key, unless its sending domain (or, for a key of
	// no domain, the keys of none) already has max live keys: then
	// ErrLimit. A max of 0 is no limit.
	CreateKey(ctx context.Context, k Key, max int) (Key, error)
	// KeyByPrefix answers ErrNotFound for a prefix no key has.
	KeyByPrefix(ctx context.Context, prefix string) (Key, error)
	// ListKeys answers keys newest first: those bound to a sending
	// domain, or every key for "". At most limit of them; 0 for all.
	ListKeys(ctx context.Context, domain string, limit int) ([]Key, error)
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

// Kinds of recipient.
const (
	KindTo  = "to"
	KindCc  = "cc"
	KindBcc = "bcc"
)

// Addressee is an address an email is to go to, and as which kind.
type Addressee struct{ Address, Kind string }

// To is some addresses as an email's "to".
func To(addresses ...string) []Addressee {
	out := make([]Addressee, len(addresses))
	for i, a := range addresses {
		out[i] = Addressee{Address: a, Kind: KindTo}
	}
	return out
}

// Recipient is one address of an email and what became of the email
// there: each address has its own outcome.
type Recipient struct {
	Address, Kind, State string
	UpdatedAt            time.Time
}

// Message is an email as it is kept: who, to whom, by which upstream and
// what became of it. Never its subject or its body: those are kept
// apart, and only when the operator chose to.
//
// An email to several addresses is kept as one row for each, and a
// Message is one of them: Recipient, Kind and State are that address's.
// EmailID names the email, and is the ID of its first address's row.
// Where a Message is answered as the email (Message, Record, Messages),
// it is that first row, and Recipients holds every address.
type Message struct {
	ID         string
	EmailID    string
	KeyID      string
	Sender     string
	Recipient  string
	Kind       string
	Recipients []Recipient
	// LeftOut are the addresses the email was asked to go to and did
	// not, being suppressed when it was sent.
	LeftOut    []string
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
	// Suppressed: every recipient is on the sending domain's suppression
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
	// Recipients are, for Started, the addresses the email goes to and
	// as what (see Addressed). Dropped are, for Started and Replay, the
	// addresses left out because they are suppressed.
	Recipients []Addressee
	Dropped    []string
}

// Start is a send about to be made.
type Start struct {
	KeyID  string
	Sender string
	// Recipients is every address the email is to go to, the first "to"
	// first: at least one.
	Recipients []Addressee
	Driver     string
	// IdemKey is the caller's Idempotency-Key, "" for none.
	IdemKey     string
	Fingerprint []byte
	// TTL is how long a key is remembered; InDoubt how long a try with no
	// answer blocks the next one.
	TTL, InDoubt time.Duration
	// Content answers what the email said, to be kept with its message
	// for KeepContent; nil when it is not kept, which is the rule unless
	// the operator chose otherwise. It is asked only when a message is
	// recorded, and the store keeps the answer as it comes, unread.
	Content     func() []byte
	KeepContent time.Duration
}

// Mail keeps messages and the idempotency keys they were sent under.
type Mail interface {
	// Start records a message as sending and claims its Idempotency-Key, in
	// one step, so that of two racing requests exactly one is Started.
	Start(ctx context.Context, s Start) (Claim, error)
	// Sent marks an email taken by the upstream, for every address of
	// it, and its key done. Lost and Failed are of the whole email too.
	Sent(ctx context.Context, id, upstreamID string) error
	// Lost marks a message whose upstream answer never came: its fate is
	// unknown. Its key stays in flight until the in-doubt window passes.
	Lost(ctx context.Context, id string) error
	// Failed marks a message the upstream certainly did not take, and frees
	// its key so the next try sends at once.
	Failed(ctx context.Context, id string) error
	// Message answers an email by its id; ErrNotFound for an id no
	// email has.
	Message(ctx context.Context, id string) (Message, error)
	// Messages lists emails, newest first: those with an address that
	// fits the query, where it names an address or a state.
	Messages(ctx context.Context, q MessageQuery) ([]Message, error)
	// Content answers what a message said and until when it is shown: if
	// it was kept, its own time is not up, and it has been kept for less
	// than keep. Nil otherwise: what is past its time is not answered
	// even before it is pruned, nor what a longer setting once kept.
	Content(ctx context.Context, id string, keep time.Duration) ([]byte, time.Time, error)
}

// Store is everything, as the composition root holds it.
type Store interface {
	Keys
	Mail
	Events
	Suppressions
	Webhooks
	Pruning
	Sessions
	Domains
	Counts
	// Ping reports whether the store can be reached.
	Ping(ctx context.Context) error
}
