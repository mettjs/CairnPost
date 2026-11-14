package mail

import (
	"fmt"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/store"
)

// Request is a send as a caller asks for it.
type Request struct {
	From string
	// To, Cc and Bcc are who it goes to: one "to" at least.
	To, Cc, Bcc []string
	ReplyTo     string
	Subject     string
	HTML        string
	Text        string
	Headers     map[string]string
	// Attachments are the files to send with it.
	Attachments []Attachment
	// IdemKey is the Idempotency-Key header, "" for none.
	IdemKey string
	// ScheduledAt is when to send it; zero for now.
	ScheduledAt time.Time
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
	// maxAddress is the longest address there can be: 64 bytes before
	// the "@", 255 after. Longer is not an address any server takes, and
	// an address is kept in indexes, which hold only so much.
	maxAddress = 320
	// maxName is the longest name a sender may have.
	maxName = 320
	// maxWord is the longest run without a space a subject or a header
	// may have when it is plain ASCII: a mail server takes lines of 998
	// bytes, and a line can be broken only at a space. Other text is
	// written in an encoded form that breaks anywhere.
	maxWord = 900
)

// unbroken reports whether a value written as it is would make a line
// no mail server takes.
func unbroken(value string) bool {
	run := 0
	for i := range len(value) {
		switch c := value[i]; {
		case c < 0x20 || c > 0x7e:
			return false // written encoded
		case c == ' ':
			run = 0
		default:
			if run++; run > maxWord {
				return true
			}
		}
	}
	return false
}

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
	// at is when to send it; zero for now.
	at time.Time
}

// addressees are a message's addresses in the order they are kept: "to",
// then "cc", then "bcc". The first "to" names the email.
func addressees(m driver.Message) []store.Addressee {
	var out []store.Addressee
	for _, of := range []struct {
		kind string
		list []string
	}{{store.KindTo, m.To}, {store.KindCc, m.Cc}, {store.KindBcc, m.Bcc}} {
		for _, a := range of.list {
			out = append(out, store.Addressee{Address: a, Kind: of.kind})
		}
	}
	return out
}

// recipients reads the addresses of "to", "cc" and "bcc" into their wire
// form: one "to" at least, most in all, none twice.
func recipients(r Request, most int) (to, cc, bcc []string, err error) {
	if len(r.To) == 0 {
		return nil, nil, nil, &Invalid{"to", `"to" must be an address, or a list of them: an email goes to someone.`}
	}
	if n := len(r.To) + len(r.Cc) + len(r.Bcc); n > most {
		return nil, nil, nil, &Invalid{"to", fmt.Sprintf(`An email goes to at most %d addresses, counting "to", "cc" and "bcc"; this one names %d.`, most, n)}
	}
	seen := map[string]bool{}
	read := func(field string, typed []string) ([]string, error) {
		var out []string
		for _, one := range typed {
			a, err := netmail.ParseAddress(one)
			if err != nil {
				return nil, &Invalid{field, fmt.Sprintf(`Every address in %q must be an address, such as "ana@example.org".`, field)}
			}
			wire := address.Wire(a)
			if len(wire) > maxAddress {
				return nil, &Invalid{field, fmt.Sprintf(`An address in %q must be at most 320 bytes.`, field)}
			}
			if folded := store.Fold(wire); seen[folded] {
				return nil, &Invalid{field, `An address is named more than once in "to", "cc" and "bcc"; name each once.`}
			} else {
				seen[folded] = true
			}
			out = append(out, wire)
		}
		return out, nil
	}
	if to, err = read("to", r.To); err != nil {
		return nil, nil, nil, err
	}
	if cc, err = read("cc", r.Cc); err != nil {
		return nil, nil, nil, err
	}
	bcc, err = read("bcc", r.Bcc)
	return to, cc, bcc, err
}

// check validates a request. Its error is always an *Invalid.
func check(r Request, opt Options) (checked, error) {
	from, err := netmail.ParseAddress(r.From)
	if err != nil {
		return checked{}, &Invalid{"from", `"from" must be an address: "Name <you@example.com>" or "you@example.com".`}
	}
	if strings.ContainsFunc(from.Name, unicode.IsControl) {
		// A name is written into a header by whoever delivers; a line break
		// in it would be a header of the caller's choosing.
		return checked{}, &Invalid{"from", `The sender's name in "from" must not contain line breaks or control characters.`}
	}
	if len(from.Name) > maxName {
		return checked{}, &Invalid{"from", `The sender's name in "from" must be at most 320 bytes.`}
	}
	if len(address.Wire(from)) > maxAddress {
		return checked{}, &Invalid{"from", `The address in "from" must be at most 320 bytes.`}
	}
	to, cc, bcc, err := recipients(r, opt.MaxRecipients)
	if err != nil {
		return checked{}, err
	}
	reply := ""
	if r.ReplyTo != "" {
		a, err := netmail.ParseAddress(r.ReplyTo)
		if err != nil {
			return checked{}, &Invalid{"reply_to", `"reply_to" must be an address.`}
		}
		if reply = address.Wire(a); len(reply) > maxAddress {
			return checked{}, &Invalid{"reply_to", `The address in "reply_to" must be at most 320 bytes.`}
		}
	}
	switch {
	case strings.TrimSpace(r.Subject) == "":
		return checked{}, &Invalid{"subject", `"subject" must not be empty.`}
	case len(r.Subject) > maxSubject || strings.ContainsAny(r.Subject, "\r\n"):
		return checked{}, &Invalid{"subject", `"subject" must be one line of at most 998 bytes.`}
	case unbroken(r.Subject):
		return checked{}, &Invalid{"subject", `"subject" must have a space at least every 900 characters.`}
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
		case reserved[strings.ToLower(name)], strings.HasPrefix(strings.ToLower(name), "content-"):
			return checked{}, &Invalid{"headers", fmt.Sprintf("The %q header is set by its own field or by the mail system; remove it from \"headers\".", name)}
		case len(value) > maxHeaderValue || strings.ContainsAny(value, "\r\n"):
			return checked{}, &Invalid{"headers", fmt.Sprintf("The %q header must be one line of at most 2048 bytes.", name)}
		case unbroken(value):
			return checked{}, &Invalid{"headers", fmt.Sprintf("The %q header must have a space at least every 900 characters.", name)}
		}
	}
	files, err := attachments(r, opt.MaxAttachments, opt.MaxAttachmentBytes, !opt.noInline)
	if err != nil {
		return checked{}, err
	}
	if at := r.ScheduledAt; !at.IsZero() {
		switch now := opt.Now(); {
		case !at.After(now):
			return checked{}, &Invalid{"scheduled_at", `"scheduled_at" must be a time to come. Leave it out to send now.`}
		case at.Sub(now) > opt.ScheduleAhead:
			return checked{}, &Invalid{"scheduled_at", fmt.Sprintf(`"scheduled_at" may be at most %s from now.`, span(opt.ScheduleAhead))}
		}
	}
	return checked{
		msg: driver.Message{From: address.Wire(from), FromName: from.Name, To: to, Cc: cc, Bcc: bcc, ReplyTo: reply,
			Subject: r.Subject, HTML: r.HTML, Text: r.Text, Headers: r.Headers, Attachments: files},
		domain: store.DomainOf(from.Address), at: r.ScheduledAt,
	}, nil
}

// span says a length of time in days, or in hours when it is not whole
// days.
func span(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return fmt.Sprintf("%d hours", d/time.Hour)
}
