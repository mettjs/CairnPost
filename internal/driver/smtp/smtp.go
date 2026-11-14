// Package smtp is the driver for any mail server that takes mail by
// SMTP: one's own, or a provider's relay. It is the way out for whoever
// has no driver of their own here.
//
// Such a server says only whether it took the email. What became of it
// afterwards (delivered, bounced) it does not tell this driver, so an
// email's record stops at "sent" and nothing is suppressed by itself.
package smtp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/message"
)

// Name is the driver's name.
const Name = "smtp"

// How the connection is secured.
const (
	// StartTLS begins in the clear and must be upgraded before anything
	// is said: a server that does not offer it is not used.
	StartTLS = "starttls"
	// TLS is secured from the first byte.
	TLS = "tls"
	// None is never secured: for a server on the same machine or network.
	None = "none"
)

// Options say which server, and how to speak to it.
type Options struct {
	Host string
	Port int
	// Security is StartTLS, TLS or None.
	Security string
	// Username and Password sign in; "" for a server that asks nobody.
	Username, Password string
	// Hello is the name this side gives itself.
	Hello string
}

// Driver sends through one server.
type Driver struct {
	opt Options
	now func() time.Time
	// dial opens the connection; a test's own in tests.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	// roots are the authorities a server's certificate is checked
	// against; nil for the system's.
	tls *tls.Config
}

// New returns the driver.
func New(opt Options, now func() time.Time) *Driver {
	return &Driver{opt: opt, now: now, dial: (&net.Dialer{}).DialContext, tls: &tls.Config{ServerName: opt.Host, MinVersion: tls.VersionTLS12}}
}

var _ driver.Driver = (*Driver)(nil)

// Name implements driver.Driver.
func (*Driver) Name() string { return Name }

// Send implements driver.Driver. The id it answers is the Message-ID it
// gave the email.
//
// Whether an answer is the email's fault goes by when it came. Before
// the email is named (the greeting, the sign-in) a refusal is the
// server's setup, and nothing was sent: try later. At the sender, a
// recipient or the content, a refusal for good is the email's. Once the
// content was handed over whole, an answer that never comes leaves it
// open whether the email went.
func (d *Driver) Send(ctx context.Context, m driver.Message) (string, error) {
	id, err := d.messageID(m.From)
	if err != nil {
		return "", fmt.Errorf("smtp: make a message id: %w: %w", err, driver.ErrUnavailable)
	}
	var raw bytes.Buffer
	if err := message.Write(&raw, m, message.Stamp{At: d.now(), ID: id, Boundary: boundary}); err != nil {
		return "", fmt.Errorf("smtp: write the message: %w: %w", err, driver.ErrRejected)
	}
	c, done, err := d.connect(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()
	defer done()
	to := slices.Concat(m.To, m.Cc, m.Bcc)
	if ok, _ := c.Extension("SMTPUTF8"); !ok && !plainASCII(append(to, m.From, m.ReplyTo)...) {
		// Such an address is carried only by a server that says it can.
		return "", fmt.Errorf("smtp: an address is not plain ASCII and the server does not take such: %w", driver.ErrRejected)
	}
	if err := c.Mail(m.From); err != nil {
		return "", said("the sender", err, true)
	}
	// One address the server will not take refuses the email: nothing
	// ever tells this driver what became of the others.
	for _, one := range to {
		if err := c.Rcpt(one); err != nil {
			var reply *textproto.Error
			if errors.As(err, &reply) && reply.Code == 552 {
				// "Too many recipients", which a server is to be asked
				// again about later, not believed for good.
				return "", fmt.Errorf("smtp: the server answered 552 at a recipient: %w", driver.ErrUnavailable)
			}
			return "", said("a recipient", err, true)
		}
	}
	w, err := c.Data()
	if err != nil {
		return "", said("the content", err, true)
	}
	if _, err := w.Write(raw.Bytes()); err != nil {
		// Cut off midway: a server does not send half a message.
		return "", fmt.Errorf("smtp: the connection ended while the message was handed over: %w", driver.ErrUnavailable)
	}
	if err := w.Close(); err != nil {
		var reply *textproto.Error
		switch {
		case errors.As(err, &reply) && reply.Code >= 200 && reply.Code < 300:
			// Taken, in other words than the usual.
		case errors.As(err, &reply) && reply.Code >= 400 && reply.Code < 600:
			return "", said("the message", err, true)
		default:
			// Handed over whole, and no answer that says yes or no: it
			// may have gone.
			return "", errors.New("smtp: the server gave no clear answer after the message was handed over")
		}
	}
	_ = c.Quit() // taken already, whatever the goodbye
	return "<" + id + ">", nil
}

// said is what a server's answer means. Its words are never carried:
// they can name an address. about is true where the answer is about the
// email; elsewhere any refusal is the server's own state.
func said(stage string, err error, about bool) error {
	var reply *textproto.Error
	switch {
	case !errors.As(err, &reply):
		return fmt.Errorf("smtp: the connection failed at %s: %w", stage, driver.ErrUnavailable)
	case about && reply.Code >= 500 && !slices.Contains(ofTheSession, reply.Code):
		return fmt.Errorf("smtp: the server refused %s (%d): %w", stage, reply.Code, driver.ErrRejected)
	}
	return fmt.Errorf("smtp: the server answered %d at %s: %w", reply.Code, stage, driver.ErrUnavailable)
}

// ofTheSession are refusals for good that are about how this side
// speaks to the server, whenever they come: a command it does not take,
// out of turn, or a sign-in it wants or refuses. The email is not at
// fault, and another try, once the setting is right, may go.
var ofTheSession = []int{502, 503, 504, 530, 534, 535, 538}

// messageID is a new Message-ID for an email of a sender: random, under
// the sender's domain, or under this side's own name where the domain
// is not plain ASCII, which an id cannot hold.
func (d *Driver) messageID(from string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	domain := from[strings.LastIndexByte(from, '@')+1:]
	if !plainASCII(domain) {
		domain = d.opt.Hello
	}
	return hex.EncodeToString(b) + "@" + domain, nil
}

// plainASCII reports whether every one of some texts is printable ASCII.
func plainASCII(texts ...string) bool {
	for _, t := range texts {
		for i := range len(t) {
			if t[i] < 0x20 || t[i] > 0x7e {
				return false
			}
		}
	}
	return true
}

// boundary is a line no content holds: 24 random bytes.
func boundary() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b) // never fails on a system that runs
	return "=_" + hex.EncodeToString(b)
}

// address is the server's, as a dialer takes it.
func (d *Driver) address() string { return net.JoinHostPort(d.opt.Host, strconv.Itoa(d.opt.Port)) }
