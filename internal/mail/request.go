package mail

import (
	"fmt"
	netmail "net/mail"
	"regexp"
	"strings"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Request is a send as a caller asks for it.
type Request struct {
	From    string
	To      []string
	ReplyTo string
	Subject string
	HTML    string
	Text    string
	Headers map[string]string
	// IdemKey is the Idempotency-Key header, "" for none.
	IdemKey string
}

// Invalid is a request that can never be sent as it is: the caller must
// change it. Field names the part at fault.
type Invalid struct{ Field, Reason string }

func (e *Invalid) Error() string { return fmt.Sprintf("mail: %s: %s", e.Field, e.Reason) }

// Limits a message is held to whatever the upstream.
const (
	maxSubject     = 998
	maxHeaderValue = 2048
	maxIdemKey     = 256
)

var headerName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// reserved are the headers a caller may not set: each is a field of the
// request, or the upstream's to write.
var reserved = map[string]bool{
	"from": true, "to": true, "cc": true, "bcc": true, "subject": true, "reply-to": true,
	"date": true, "message-id": true, "mime-version": true, "content-type": true,
	"content-transfer-encoding": true, "dkim-signature": true, "return-path": true, "received": true,
}

// checked is a request found sendable: the message for the driver, and the
// bare addresses the record keeps.
type checked struct {
	msg               driver.Message
	sender, recipient string
	domain            string
}

// check validates a request. Its error is always an *Invalid.
func check(r Request) (checked, error) {
	from, err := netmail.ParseAddress(r.From)
	if err != nil {
		return checked{}, &Invalid{"from", `must be an address, "Name <you@example.com>" or bare`}
	}
	if len(r.To) != 1 {
		return checked{}, &Invalid{"to", "must be exactly one address; one request sends one email"}
	}
	to, err := netmail.ParseAddress(r.To[0])
	if err != nil {
		return checked{}, &Invalid{"to", "must be an address"}
	}
	reply := ""
	if r.ReplyTo != "" {
		a, err := netmail.ParseAddress(r.ReplyTo)
		if err != nil {
			return checked{}, &Invalid{"reply_to", "must be an address"}
		}
		reply = a.Address
	}
	switch {
	case strings.TrimSpace(r.Subject) == "":
		return checked{}, &Invalid{"subject", "must not be empty"}
	case len(r.Subject) > maxSubject || strings.ContainsAny(r.Subject, "\r\n"):
		return checked{}, &Invalid{"subject", "must be one line of at most 998 bytes"}
	case r.HTML == "" && r.Text == "":
		return checked{}, &Invalid{"html", "give html, text or both"}
	case len(r.IdemKey) > maxIdemKey:
		return checked{}, &Invalid{"Idempotency-Key", "must be at most 256 bytes"}
	}
	for name, value := range r.Headers {
		switch {
		case !headerName.MatchString(name):
			return checked{}, &Invalid{"headers", fmt.Sprintf("%q is not a header name", name)}
		case reserved[strings.ToLower(name)]:
			return checked{}, &Invalid{"headers", fmt.Sprintf("%q is set by its own field or by the mail system", name)}
		case len(value) > maxHeaderValue || strings.ContainsAny(value, "\r\n"):
			return checked{}, &Invalid{"headers", fmt.Sprintf("%q must be one line of at most 2048 bytes", name)}
		}
	}
	domain := strings.ToLower(from.Address[strings.LastIndex(from.Address, "@")+1:])
	return checked{
		msg: driver.Message{From: from.String(), To: to.Address, ReplyTo: reply,
			Subject: r.Subject, HTML: r.HTML, Text: r.Text, Headers: r.Headers},
		sender: from.Address, recipient: to.Address, domain: domain,
	}, nil
}
