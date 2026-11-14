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
	"fmt"
	"io"
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

// Write writes m as one message.
func Write(w io.Writer, m driver.Message, s Stamp) error {
	var out bytes.Buffer
	from := netmail.Address{Name: m.FromName, Address: m.From}
	field(&out, "Date", s.At.Format(time.RFC1123Z))
	field(&out, "From", from.String())
	field(&out, "To", strings.Join(m.To, ", "))
	if len(m.Cc) > 0 {
		field(&out, "Cc", strings.Join(m.Cc, ", "))
	}
	if m.ReplyTo != "" {
		field(&out, "Reply-To", m.ReplyTo)
	}
	field(&out, "Subject", words(m.Subject))
	field(&out, "Message-ID", "<"+s.ID+">")
	field(&out, "MIME-Version", "1.0")
	names := make([]string, 0, len(m.Headers))
	for name := range m.Headers {
		names = append(names, name)
	}
	slices.Sort(names) // the same message is the same bytes
	for _, name := range names {
		field(&out, name, words(m.Headers[name]))
	}
	if err := whole(m).write(&out, s.Boundary); err != nil {
		return err
	}
	_, err := w.Write(out.Bytes())
	return err
}

// words is a header's value as a header can carry it: as it is when it
// is plain, else as encoded words.
func words(s string) string {
	return mime.QEncoding.Encode("utf-8", s)
}

// lineLength is how long a header's line is kept where it can be.
const lineLength = 76

// field writes one header, broken at its spaces into lines a server
// takes. A word longer than a line is left whole.
func field(out *bytes.Buffer, name, value string) {
	line := len(name) + 2
	fmt.Fprintf(out, "%s: ", name)
	for i, word := range strings.Split(value, " ") {
		switch {
		case i == 0:
		case line+1+len(word) > lineLength:
			out.WriteString("\r\n ")
			line = 1
		default:
			out.WriteByte(' ')
			line++
		}
		out.WriteString(word)
		line += len(word)
	}
	out.WriteString("\r\n")
}
