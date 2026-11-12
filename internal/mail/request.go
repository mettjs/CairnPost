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
// change it. Field names the part at fault; Reason is a whole sentence
// saying what it must be.
type Invalid struct{ Field, Reason string }

func (e *Invalid) Error() string { return "mail: " + e.Reason }

// Limits a message is held to whatever the upstream.
const (
	maxSubject     = 998
	maxHeaderValue = 2048
	maxIdemKey     = 256
)

var (
	headerName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	// An Idempotency-Key is printable ASCII, so it is the same bytes in a
	// header, a log and the database.
	idemKey = regexp.MustCompile(`^[\x20-\x7E]*$`)
)

// reserved are the headers a caller may not set: each is a field of the
// request, or the upstream's to write.
var reserved = map[string]bool{
	"from": true, "to": true, "cc": true, "bcc": true, "subject": true, "reply-to": true,
	"date": true, "message-id": true, "mime-version": true, "content-type": true,
	"content-transfer-encoding": true, "dkim-signature": true, "return-path": true, "received": true,
}

// checked is a request found sendable: the message for the driver, and the
// sender's domain.
type checked struct {
	msg    driver.Message
	domain string
}

// bare is an address alone, as it is written on the wire: a local part
// that needs quotes keeps them.
func bare(a *netmail.Address) string {
	s := (&netmail.Address{Address: a.Address}).String()
	return strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
}

// check validates a request. Its error is always an *Invalid.
func check(r Request) (checked, error) {
	from, err := netmail.ParseAddress(r.From)
	if err != nil {
		return checked{}, &Invalid{"from", `"from" must be an address: "Name <you@example.com>" or "you@example.com".`}
	}
	if len(r.To) != 1 {
		return checked{}, &Invalid{"to", `"to" must be exactly one address: one request sends one email.`}
	}
	to, err := netmail.ParseAddress(r.To[0])
	if err != nil {
		return checked{}, &Invalid{"to", `"to" must be an address, such as "ana@example.org".`}
	}
	reply := ""
	if r.ReplyTo != "" {
		a, err := netmail.ParseAddress(r.ReplyTo)
		if err != nil {
			return checked{}, &Invalid{"reply_to", `"reply_to" must be an address.`}
		}
		reply = bare(a)
	}
	switch {
	case strings.TrimSpace(r.Subject) == "":
		return checked{}, &Invalid{"subject", `"subject" must not be empty.`}
	case len(r.Subject) > maxSubject || strings.ContainsAny(r.Subject, "\r\n"):
		return checked{}, &Invalid{"subject", `"subject" must be one line of at most 998 bytes.`}
	case r.HTML == "" && r.Text == "":
		return checked{}, &Invalid{"html", `Give "html", "text" or both: an email needs a body.`}
	case len(r.IdemKey) > maxIdemKey || !idemKey.MatchString(r.IdemKey):
		return checked{}, &Invalid{"Idempotency-Key",
			"The Idempotency-Key header must be at most 256 printable ASCII characters."}
	}
	for name, value := range r.Headers {
		switch {
		case !headerName.MatchString(name):
			return checked{}, &Invalid{"headers", fmt.Sprintf("%q is not a header name: use letters, digits and hyphens.", name)}
		case reserved[strings.ToLower(name)]:
			return checked{}, &Invalid{"headers", fmt.Sprintf("The %q header is set by its own field or by the mail system; remove it from \"headers\".", name)}
		case len(value) > maxHeaderValue || strings.ContainsAny(value, "\r\n"):
			return checked{}, &Invalid{"headers", fmt.Sprintf("The %q header must be one line of at most 2048 bytes.", name)}
		}
	}
	return checked{
		msg: driver.Message{From: bare(from), FromName: from.Name, To: bare(to), ReplyTo: reply,
			Subject: r.Subject, HTML: r.HTML, Text: r.Text, Headers: r.Headers},
		domain: strings.ToLower(from.Address[strings.LastIndex(from.Address, "@")+1:]),
	}, nil
}
