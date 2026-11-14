package smtp

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

var email = driver.Message{From: "app@example.com", FromName: "App", To: []string{"ana@example.org"}, Cc: []string{"bo@example.org"},
	Bcc: []string{"hidden@example.org"}, Subject: "Hello", Text: "Body"}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

// An email is told to the server as mail is: the sender, each address,
// the hidden ones too, then the message, which names no hidden one.
// The id answered is the Message-ID it was given.
func TestAnEmailIsHandedOver(t *testing.T) {
	s := &server{}
	id, err := driverFor(t, s, None, "", "").Send(ctx(t), email)
	if err != nil || !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.com>") {
		t.Fatalf("%q %v", id, err)
	}
	said := s.lines()
	for _, want := range []string{"EHLO cairnpost.test", "MAIL FROM:<app@example.com>", "RCPT TO:<ana@example.org>",
		"RCPT TO:<bo@example.org>", "RCPT TO:<hidden@example.org>", "QUIT"} {
		if !strings.Contains(said, want) {
			t.Errorf("the server was not told %q:\n%s", want, said)
		}
	}
	if !strings.Contains(s.seen().content, "Message-ID: "+id) || !strings.Contains(s.seen().content, "Subject: Hello") ||
		!strings.Contains(s.seen().content, "To: ana@example.org") || strings.Contains(s.seen().content, "hidden@example.org") {
		t.Fatalf("the message:\n%s", s.seen().content)
	}
}

// What an answer means goes by when it came: the server's own state
// before the email is named, the email's fault at its sender, a
// recipient or its content; and no words of the server are carried.
func TestEachAnswerHasItsKind(t *testing.T) {
	for name, c := range map[string]struct {
		step, answer string
		kind         error // nil for "may have gone"
		open         bool
	}{
		"a greeting that refuses":     {"greeting", "554 no service for ana@example.org", driver.ErrUnavailable, false},
		"a hello that refuses":        {"EHLO", "550 go away ana@example.org", driver.ErrUnavailable, false},
		"a wrong password":            {"AUTH", "535 bad credentials for ana@example.org", driver.ErrUnavailable, false},
		"a sender refused":            {"MAIL", "550 ana@example.org not allowed", driver.ErrRejected, false},
		"a sender refused for now":    {"MAIL", "451 later ana@example.org", driver.ErrUnavailable, false},
		"a recipient refused":         {"RCPT", "550 no such user ana@example.org", driver.ErrRejected, false},
		"a recipient refused for now": {"RCPT", "450 mailbox busy ana@example.org", driver.ErrUnavailable, false},
		"the content refused unseen":  {"DATA", "554 no ana@example.org", driver.ErrRejected, false},
		"the message refused":         {"end", "552 too large for ana@example.org", driver.ErrRejected, false},
		"the message refused for now": {"end", "451 try later ana@example.org", driver.ErrUnavailable, false},
	} {
		s := &server{offers: []string{"AUTH PLAIN"}, answers: map[string]string{c.step: c.answer}}
		_, err := driverFor(t, s, None, "user", "PASSWORDMARK").Send(ctx(t), email)
		if err == nil || !errors.Is(err, c.kind) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, never := range []string{"ana@example.org", "PASSWORDMARK", "user"} {
			if strings.Contains(err.Error(), never) {
				t.Errorf("%s: the error says %q: %v", name, never, err)
			}
		}
		if c.step != "end" && s.seen().content != "" {
			t.Errorf("%s: the message was handed over all the same", name)
		}
	}
	// A refusal that is about how this side speaks is not the email's,
	// wherever it comes; nor is "too many recipients".
	for name, c := range map[string][2]string{
		"a sign-in wanted, at the sender":      {"MAIL", "530 5.7.0 authentication required"},
		"a hello refused, at the recipient":    {"RCPT", "504 5.5.2 helo rejected"},
		"too many recipients":                  {"RCPT", "552 too many recipients"},
		"commands out of turn, at the content": {"DATA", "503 bad sequence"},
	} {
		s := &server{answers: map[string]string{c[0]: c[1]}}
		if _, err := driverFor(t, s, None, "", "").Send(ctx(t), email); !errors.Is(err, driver.ErrUnavailable) || errors.Is(err, driver.ErrRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// "Taken", said in another number than the usual, is taken.
	other := &server{answers: map[string]string{"end": "251 forwarded"}}
	if id, err := driverFor(t, other, None, "", "").Send(ctx(t), email); err != nil || id == "" {
		t.Fatalf("a 251 after the message: %q %v", id, err)
	}
	// Handed over whole and never answered: it may have gone, which is
	// neither of the two kinds that say nothing was sent.
	s := &server{hangAtEnd: true}
	_, err := driverFor(t, s, None, "", "").Send(ctx(t), email)
	if err == nil || errors.Is(err, driver.ErrRejected) || errors.Is(err, driver.ErrUnavailable) || s.seen().content == "" {
		t.Fatalf("no answer after the message: %v", err)
	}
	// A server that is not there sent nothing.
	gone := New(Options{Host: "127.0.0.1", Port: 1, Security: None}, time.Now)
	if _, err := gone.Send(ctx(t), email); !errors.Is(err, driver.ErrUnavailable) {
		t.Fatalf("no server: %v", err)
	}
}

// The connection is secured before anything is said, a server that
// will not secure it is told nothing, and the sign-in is whichever way
// the server offers.
func TestSecuringAndSigningIn(t *testing.T) {
	s := &server{offers: []string{"STARTTLS", "AUTH LOGIN"}, cert: certified(t)}
	if _, err := driverFor(t, s, StartTLS, "user@example.com", "secret word").Send(ctx(t), email); err != nil {
		t.Fatal(err)
	}
	if !s.seen().secured || s.seen().user != "user@example.com" || s.seen().password != "secret word" || s.seen().content == "" {
		t.Fatalf("secured %v, signed in as %q", s.seen().secured, s.seen().user)
	}
	plain := &server{offers: []string{"AUTH LOGIN PLAIN"}}
	if _, err := driverFor(t, plain, None, "u", "p").Send(ctx(t), email); err != nil || plain.seen().user != "u" || plain.seen().password != "p" ||
		!strings.Contains(plain.lines(), "AUTH PLAIN") {
		t.Fatalf("by PLAIN: %v %q", err, plain.seen().user)
	}
	// No offer to secure: nothing more is said, least of all a password.
	bare := &server{offers: []string{"AUTH PLAIN"}}
	_, err := driverFor(t, bare, StartTLS, "u", "PASSWORDMARK").Send(ctx(t), email)
	if !errors.Is(err, driver.ErrUnavailable) || strings.Contains(bare.lines(), "AUTH") || strings.Contains(bare.lines(), "MAIL") {
		t.Fatalf("with no way to secure: %v\n%s", err, bare.lines())
	}
	// A way to sign in that is not known here is not guessed at.
	odd := &server{offers: []string{"AUTH XOAUTH2"}}
	if _, err := driverFor(t, odd, None, "u", "p").Send(ctx(t), email); !errors.Is(err, driver.ErrUnavailable) || strings.Contains(odd.lines(), "MAIL") {
		t.Fatalf("an unknown sign-in: %v", err)
	}
}

// A server that stops answering does not hold a send past its time.
func TestASilentServerEndsWithTheCallersTime(t *testing.T) {
	s := &server{answers: map[string]string{"greeting": ""}}
	d := driverFor(t, s, None, "", "")
	d.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		a, b := net.Pipe() // nobody ever writes to b
		t.Cleanup(func() { a.Close(); b.Close() })
		return a, nil
	}
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, err := d.Send(short, email); !errors.Is(err, driver.ErrUnavailable) || time.Since(began) > 3*time.Second {
		t.Fatalf("%v after %v", err, time.Since(began))
	}
}

// An address that is not plain ASCII goes only through a server that
// says it carries such; through another the email is refused before
// the server is told of it. And no setting of this driver is read as
// "in the clear" that does not say so.
func TestWhatIsNotPlainAndWhatIsNotSet(t *testing.T) {
	wide := email
	wide.To = []string{"añá@exämple.org"}
	wide.From = "josé@exämple.org"
	s := &server{}
	d := driverFor(t, s, None, "", "")
	if _, err := d.Send(ctx(t), wide); !errors.Is(err, driver.ErrRejected) || strings.Contains(s.lines(), "MAIL") ||
		strings.Contains(err.Error(), "exämple") {
		t.Fatalf("through a server that takes none such: %v\n%s", err, s.lines())
	}
	able := &server{offers: []string{"SMTPUTF8"}}
	id, err := driverFor(t, able, None, "", "").Send(ctx(t), wide)
	if err != nil || !plainASCII(id) || !strings.HasSuffix(id, "@cairnpost.test>") || !strings.Contains(able.lines(), "SMTPUTF8") {
		t.Fatalf("through one that does: %q %v\n%s", id, err, able.lines())
	}
	for _, unset := range []string{"", "STARTTLS", "ssl"} {
		bare := &server{offers: []string{"AUTH PLAIN"}}
		_, err := driverFor(t, bare, unset, "u", "PASSWORDMARK").Send(ctx(t), email)
		if !errors.Is(err, driver.ErrUnavailable) || bare.lines() != "" {
			t.Errorf("security %q: %v, the server was told %q", unset, err, bare.lines())
		}
	}
	// A send that is called off ends, with no time set for it.
	silent := driverFor(t, &server{}, None, "", "")
	silent.dial = func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		go func() { // greets, then says no more
			b.Write([]byte("220 ready\r\n"))
			buf := make([]byte, 512)
			for {
				if _, err := b.Read(buf); err != nil {
					return
				}
			}
		}()
		return a, nil
	}
	off, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := silent.Send(off, email); done <- err }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, driver.ErrUnavailable) {
			t.Fatalf("called off: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a send that was called off went on waiting")
	}
}
