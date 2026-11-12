// Package driver is the seam to whoever delivers the mail. Each upstream is
// a subpackage, and only that subpackage names its host.
package driver

import (
	"context"
	"errors"
	"time"
)

// Message is one email to one recipient, as an upstream needs it.
//
// Every address is a bare address ("ana@example.org"), ready to be used as
// it is; the sender's display name travels apart, as the caller wrote it,
// for the driver to encode the way its upstream wants.
type Message struct {
	From     string
	FromName string // "" for none
	To       string
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
	// taken it. Its errors come in three kinds, and a caller acts on the
	// kind alone:
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
