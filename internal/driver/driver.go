// Package driver is the seam to whoever delivers the mail. Each upstream is
// a subpackage, and only that subpackage names its host.
package driver

import (
	"context"
	"errors"
	"time"
)

// Message is one email, as an upstream needs it.
//
// Every address is a bare address ("ana@example.org"), ready to be used as
// it is; the sender's display name travels apart, as the caller wrote it,
// for the driver to encode the way its upstream wants. To holds one
// address at least; no address is in two of To, Cc and Bcc.
type Message struct {
	From     string
	FromName string // "" for none
	To       []string
	Cc       []string
	Bcc      []string
	ReplyTo  string // "" for none
	Subject  string
	HTML     string
	Text     string
	Headers  map[string]string
}

// Driver hands a message to an upstream.
type Driver interface {
	// Name is what a message's row says delivered it.
	Name() string
	// Send answers the upstream's id for the message once the upstream has
	// taken it; "" when the upstream took it without saying an id. Its
	// errors come in three kinds, and a caller acts on the kind alone:
	//
	//   - wrapping ErrRejected: the message itself is at fault and will
	//     never be taken. Nothing was sent.
	//   - wrapping ErrUnavailable (a *Throttled among them): the upstream
	//     said no for now: its limit, its outage, our credentials. Nothing
	//     was sent; trying later may work.
	//   - anything else: the answer was lost. The message may have gone.
	Send(ctx context.Context, m Message) (id string, err error)
}

// ErrRejected is an upstream refusing a message for good.
var ErrRejected = errors.New("driver: the upstream refused this message")

// ErrUnavailable is an upstream that cannot take mail now, and said so.
var ErrUnavailable = errors.New("driver: the upstream is not taking mail now")

// Throttled is an upstream at its limit. It is an ErrUnavailable.
type Throttled struct {
	// After is how long the upstream asked to wait; zero when it did not say.
	After time.Duration
}

func (*Throttled) Error() string { return "driver: the upstream's sending limit is reached" }

// Unwrap makes a Throttled an ErrUnavailable.
func (*Throttled) Unwrap() error { return ErrUnavailable }

// Quota is an upstream's sending allowance and how much of it is used.
type Quota struct {
	Limit int
	// Per is the period the limit covers, as the upstream names it ("day").
	Per  string
	Used int
	// Over is the upstream saying the allowance is spent.
	Over bool
	// ResetsAt is when the period turns over; zero when the upstream did
	// not say.
	ResetsAt time.Time
}

// Limited is a driver whose upstream can say what its allowance is.
type Limited interface {
	Quota(ctx context.Context) (Quota, error)
}

// What a DNS record of a sending domain is for.
const (
	// UseDKIM is the key receivers check an email's signature against.
	UseDKIM = "dkim"
	// UseSPF says which servers may send for the name bounces go to.
	UseSPF = "spf"
	// UseBounce is where a receiving server sends a bounce.
	UseBounce = "bounce"
)

// Record is one DNS record an upstream asks of a domain before it sends
// as it: where it is, and enough of what it holds to tell that it is
// there. The upstream's own values, a key above all, are not known here.
type Record struct {
	Use string
	// Name is the full name it is at; Type is "TXT" or "MX".
	Name, Type string
	// Include is, for an SPF record, the include it must name.
	Include string
	// Under is, for an MX record, the name every host it points at must
	// be under: the upstream's own mail servers. "" when any host will do.
	Under string
}
