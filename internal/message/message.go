// Package message writes an email as the text mail servers pass on: its
// headers, then its bodies and files as parts. It knows no server and
// no upstream; a driver that speaks SMTP hands on what it writes.
//
// What it is given was checked by whoever took the request: every
// address is bare, and no value holds a line break. It encodes what a
// header cannot carry as it is, and never writes "bcc": those addresses
// are named to the server alone.
package message

import (
	"bytes"
	"errors"
	"mime"
	netmail "net/mail"
	"slices"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Stamp is what a message is given beside what its sender wrote.
type Stamp struct {
	// At is its date.
	At time.Time
	// ID is its Message-ID, without the angle brackets.
	ID string
	// Boundary answers a new line that no part's content holds, to
	// tell the parts apart: random, and long enough never to occur.
	Boundary func() string
}

// maxLine is the longest line a mail server must take.
const maxLine = 998

// ErrLine is a message with a line longer than a mail server takes:
// a header value with no space to break it at.
var ErrLine = errors.New("message: a header is longer than a line may be and has no space to break it at")

// Write writes m as one message onto out. If it answers an error, what
// it wrote is not a message.
func Write(out *bytes.Buffer, m driver.Message, s Stamp) error {
	began := out.Len()
	field(out, "Date", s.At.Format(time.RFC1123Z))
	field(out, "From", sender(m))
	field(out, "To", strings.Join(m.To, ", "))
	if len(m.Cc) > 0 {
		field(out, "Cc", strings.Join(m.Cc, ", "))
	}
	if m.ReplyTo != "" {
		field(out, "Reply-To", m.ReplyTo)
	}
	field(out, "Subject", words(m.Subject))
	field(out, "Message-ID", "<"+s.ID+">")
	field(out, "MIME-Version", "1.0")
	names := make([]string, 0, len(m.Headers))
	for name := range m.Headers {
		names = append(names, name)
	}
	slices.Sort(names) // the same message is the same bytes
	for _, name := range names {
		field(out, name, words(m.Headers[name]))
	}
	if err := whole(m).write(out, s.Boundary); err != nil {
		return err
	}
	for line := range bytes.Lines(out.Bytes()[began:]) {
		if len(bytes.TrimRight(line, "\r\n")) > maxLine {
			return ErrLine
		}
	}
	return nil
}

// sender is the "From" header: the name as a header carries it, then
// the address as it is. The address is in the form it is sent in
// already, and is not quoted a second time.
func sender(m driver.Message) string {
	if m.FromName == "" {
		return m.From
	}
	const stand = "<a@b>"
	named := (&netmail.Address{Name: m.FromName, Address: "a@b"}).String()
	return strings.TrimSuffix(named, stand) + "<" + m.From + ">"
}

// words is a header's value as a header can carry it: as it is when it
// is plain, else as encoded words.
func words(s string) string {
	return mime.QEncoding.Encode("utf-8", s)
}

// lineLength is how long a header's line is kept where it can be.
const lineLength = 76

// field writes one header, broken before its words into lines a server
// takes. The spaces before a word go with it to its line, so the value
// reads back as it was and no line is of spaces alone; spaces at the
// value's end are let go. A word longer than a line is left whole.
func field(out *bytes.Buffer, name, value string) {
	out.WriteString(name + ":")
	line, rest := len(name)+1, strings.TrimRight(value, " ")
	for first := true; rest != ""; first = false {
		word := strings.TrimLeft(rest, " ")
		gap := rest[:len(rest)-len(word)]
		if end := strings.IndexByte(word, ' '); end >= 0 {
			word = word[:end]
		}
		rest = rest[len(gap)+len(word):]
		if gap == "" {
			gap = " " // after the name; a value never begins a word without one
		}
		if line+len(gap)+len(word) > lineLength && (!first || len(name)+2+len(word) > lineLength) {
			out.WriteString("\r\n")
			line = 0
		}
		out.WriteString(gap + word)
		line += len(gap) + len(word)
	}
	out.WriteString("\r\n")
}
