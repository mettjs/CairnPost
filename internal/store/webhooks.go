package store

import (
	"context"
	"time"
)

// Endpoint is where an application is told what became of its emails.
type Endpoint struct {
	ID  string
	URL string
	// Domain is the sending domain whose emails it hears about, "" for all.
	Domain string
	// Kinds are the kinds of event it hears about, none for all.
	Kinds       []string
	Description string
	// Secret is the key its webhooks are signed with.
	Secret    []byte
	Enabled   bool
	CreatedAt time.Time
	// FailingSince is when the first of the tries that have all failed
	// since was made; nil while the last try was answered.
	FailingSince *time.Time
	// PausedAt is when it was paused for failing too long; nil when it
	// is not. Paused, it is still owed what happens, and nothing is tried.
	PausedAt *time.Time
	// Previous are the signing keys it had before, the ones webhooks
	// are still signed with as well, the last changed first; and
	// PreviousUntil is until when the last of them signs. Both are empty
	// once that time is over.
	Previous      [][]byte
	PreviousUntil *time.Time
}

// PreviousKept is how many keys an endpoint had before sign beside its
// own at most: the ones changed last.
const PreviousKept = 4

// KindSuppressed is what an endpoint is owed that is no event of an
// email: an address put on the suppression list because of a bounce or a
// complaint about an email with no record here.
const KindSuppressed = "suppressed"

// EndpointChange is what to change of an endpoint: nil leaves a part as
// it is.
type EndpointChange struct {
	URL         *string
	Kinds       *[]string
	Description *string
	Enabled     *bool
}

// States of a delivery.
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// DeliveryStates is every state a delivery can be in.
var DeliveryStates = []string{DeliveryPending, DeliveryDelivered, DeliveryFailed}

// Delivery is one event owed to one endpoint, and how telling it went.
type Delivery struct {
	ID         string
	EndpointID string
	// Kind and MessageID are the event's; for a suppression told on its
	// own, Kind is KindSuppressed and MessageID "".
	Kind      string
	MessageID string
	State     string
	Attempts  int
	// NextAt is when a pending delivery is tried next.
	NextAt time.Time
	// LastStatus is the endpoint's last answer, 0 for none; LastFailure is
	// why the last try failed, "" when it did not.
	LastStatus  int
	LastFailure string
	CreatedAt   time.Time
	AttemptedAt *time.Time
	DeliveredAt *time.Time
}

// Due is a delivery taken to be tried now, with all a try needs.
type Due struct {
	Delivery Delivery
	Endpoint Endpoint
	Event    Event
	Message  Message
	// Suppression is set, and Event and Message are not, for a delivery
	// that tells of a suppression on its own.
	Suppression *Suppression
}

// Webhooks keeps the endpoints and what they are owed. An event recorded
// (Events.RecordEvent) is owed, in the same step, to every enabled endpoint
// that hears about its sending domain and its kind.
type Webhooks interface {
	// CreateEndpoint makes an endpoint, unless max endpoints already hear
	// about the same sending domain (or, for one that hears about all,
	// about all): then ErrLimit.
	CreateEndpoint(ctx context.Context, e Endpoint, max int) (Endpoint, error)
	// Endpoint answers ErrNotFound for an id no endpoint has.
	Endpoint(ctx context.Context, id string) (Endpoint, error)
	// ListEndpoints answers one sending domain's endpoints, or all (""),
	// oldest first.
	ListEndpoints(ctx context.Context, domain string) ([]Endpoint, error)
	// UpdateEndpoint answers the endpoint as changed; ErrNotFound for none.
	// It neither pauses nor resumes.
	UpdateEndpoint(ctx context.Context, id string, c EndpointChange) (Endpoint, error)
	// DeleteEndpoint removes an endpoint and all it was owed.
	DeleteEndpoint(ctx context.Context, id string) error

	// RotateSecret gives an endpoint a new signing key. The one it had
	// is kept beside it for overlap, with any it had before that are
	// still within their own, and webhooks are signed with them all until
	// then. ErrNotFound for none.
	RotateSecret(ctx context.Context, id string, secret []byte, overlap time.Duration) (Endpoint, error)
	// ResumeEndpoint ends an endpoint's pause: its failing is forgotten,
	// and what was given up since the failing began is owed again, one
	// more try each. It answers whether the endpoint was paused; one that
	// was not is left as it is. ErrNotFound for none.
	ResumeEndpoint(ctx context.Context, id string) (Endpoint, bool, error)
	// Owed answers how many webhooks each of some endpoints is owed:
	// those waiting to be tried and, for a paused one, those given up
	// since its failing began, which ResumeEndpoint makes wait again.
	// Counting stops at most for each. An endpoint owed none, or that is
	// not there, is not in the answer.
	Owed(ctx context.Context, ids []string, most int) (map[string]int, error)
	// EndpointAnswered records that an endpoint answered something that
	// is no delivery (a webhook sent to try it out): it is no longer
	// failing. A paused endpoint stays paused, and its failing is kept.
	EndpointAnswered(ctx context.Context, id string) error
	// TakeDeliveries takes up to max pending deliveries that are due, the
	// longest due first, of enabled endpoints that are not paused, other than those in busy, at
	// most perEndpoint of each so that one endpoint with much owed does
	// not keep the others waiting. Each is counted as tried and is the
	// taker's for lease: left unsettled, it comes due again.
	TakeDeliveries(ctx context.Context, max, perEndpoint int, busy []string, lease time.Duration) ([]Due, error)
	// DeliveryDone settles a delivery the endpoint took, whatever else
	// was recorded of it meanwhile: taken is taken. The endpoint is no
	// longer failing; one that was paused meanwhile stays paused, with
	// its failing kept for when it is resumed.
	DeliveryDone(ctx context.Context, id string, status int) error
	// DeliveryMissed settles a try that failed: tried again after retryIn,
	// or, when final, never. attempt is the try being settled, as it was
	// taken: a try that another has since overtaken settles nothing.
	//
	// The endpoint is failing from when its first try missed after one
	// that was answered was made. It is paused when a webhook is given up
	// (final) and the failing began pauseAfter ago or longer: one miss
	// after a quiet spell does not pause it, since that webhook is then
	// tried again and its tries must all fail first. paused answers true
	// that once; pauseAfter 0 never pauses.
	DeliveryMissed(ctx context.Context, id string, attempt, status int, failure string, retryIn time.Duration, final bool,
		pauseAfter time.Duration) (paused bool, err error)
	// DeliveryReleased hands back a delivery whose try was cut short: the
	// try is not counted and the delivery is due at once.
	DeliveryReleased(ctx context.Context, id string, attempt int) error
	// ListDeliveries answers an endpoint's deliveries, newest first: those
	// in a state, or all (""), older than the delivery before, or from the
	// newest ("").
	ListDeliveries(ctx context.Context, endpointID, state, before string, limit int) ([]Delivery, error)
	// RetryDelivery makes a delivery of an endpoint due now: one that was
	// given up gets one more try, one that is waiting stops waiting.
	// ErrNotFound for none such, or one already delivered.
	RetryDelivery(ctx context.Context, endpointID, id string) error
}
