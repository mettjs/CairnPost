package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	netsmtp "net/smtp"
	"slices"
	"strings"

	"github.com/mettjs/cairnpost/internal/driver"
)

// connect opens a connection ready to be told an email: greeted,
// secured as asked and signed in. Its errors all mean nothing was sent
// and the server, or how it is set here, is why.
//
// Whoever calls the send off ends the exchange, at whatever step it
// is and whether or not a time was set for it: done is to be called
// when the exchange is over.
func (d *Driver) connect(ctx context.Context) (c *netsmtp.Client, done func() bool, err error) {
	if !slices.Contains([]string{StartTLS, TLS, None}, d.opt.Security) {
		// Never taken to mean "in the clear".
		return nil, nil, fmt.Errorf("smtp: how the connection is secured is not set to a way known here: %w", driver.ErrUnavailable)
	}
	conn, err := d.dial(ctx, "tcp", d.address())
	if err != nil {
		return nil, nil, fmt.Errorf("smtp: the server was not reached: %w", driver.ErrUnavailable)
	}
	wire := conn
	stop := context.AfterFunc(ctx, func() { wire.Close() })
	defer func() {
		if err != nil {
			stop()
		}
	}()
	// The whole exchange ends with the caller's time: net/smtp takes no
	// context of its own.
	if until, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(until)
	}
	if d.opt.Security == TLS {
		secured := tls.Client(conn, d.tls)
		if err := secured.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("smtp: the connection could not be secured: %w", driver.ErrUnavailable)
		}
		conn = secured
	}
	if c, err = netsmtp.NewClient(conn, d.opt.Host); err != nil {
		conn.Close()
		return nil, nil, said("the greeting", err, false)
	}
	if err = d.ready(c); err != nil {
		c.Close()
		return nil, nil, err
	}
	return c, stop, nil
}

// ready says hello, secures a connection begun in the clear, and signs
// in. A server that does not offer what is asked of it is not used: the
// password is never said on a connection that was to be secured and is
// not.
func (d *Driver) ready(c *netsmtp.Client) error {
	if err := c.Hello(d.opt.Hello); err != nil {
		return said("the hello", err, false)
	}
	if d.opt.Security == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp: the server does not offer to secure the connection: %w", driver.ErrUnavailable)
		}
		if err := c.StartTLS(d.tls); err != nil {
			return fmt.Errorf("smtp: the connection could not be secured: %w", driver.ErrUnavailable)
		}
	}
	if d.opt.Username == "" {
		return nil
	}
	ok, offered := c.Extension("AUTH")
	ways := strings.Fields(strings.ToUpper(offered))
	var auth netsmtp.Auth
	switch {
	case ok && slices.Contains(ways, "PLAIN"):
		auth = plain{d.opt.Username, d.opt.Password}
	case ok && slices.Contains(ways, "LOGIN"):
		auth = &login{d.opt.Username, d.opt.Password}
	default:
		return fmt.Errorf("smtp: the server offers no way to sign in that is known here (PLAIN, LOGIN): %w", driver.ErrUnavailable)
	}
	if err := c.Auth(auth); err != nil {
		return said("the sign-in", err, false)
	}
	return nil
}

// plain signs in by AUTH PLAIN. Whether the connection is secured was
// settled before it is asked: that is this driver's setting, not a
// guess from the server's name.
type plain struct{ user, password string }

func (a plain) Start(*netsmtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.password), nil
}

func (plain) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("smtp: the server asked for more than a sign-in gives")
	}
	return nil, nil
}

// login signs in by AUTH LOGIN, which some servers offer alone: the
// name, then the password, each when asked.
type login struct{ user, password string }

func (*login) Start(*netsmtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a *login) Next(asked []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSuffix(strings.TrimSpace(string(asked)), ":")) {
	case "username":
		return []byte(a.user), nil
	case "password":
		return []byte(a.password), nil
	}
	return nil, errors.New("smtp: the server asked for something a sign-in does not give")
}
