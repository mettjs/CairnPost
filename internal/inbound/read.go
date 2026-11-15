package inbound

import (
	"cmp"
	"fmt"
	"maps"
	"net/mail"
	"slices"
	"strings"
	"time"
)

// What a reading holds at most: a stranger wrote the message, and a
// reading is made on every read of it. The message as it came holds
// the rest.
const (
	// maxHeaders is how many headers are shown, and maxValue how long
	// one's value.
	maxHeaders = 200
	maxValue   = 4 << 10
	// maxBody is how long the text, and the HTML, may be.
	maxBody = 1 << 20
)

// verdicts are the headers the mail provider adds to say who sent a
// message and whether that checks out: shown first, so that no number
// of other headers crowds them out.
var verdicts = []string{"Authentication-Results", "Received-Spf", "Arc-Authentication-Results"}

// cut is a text of most bytes at most, and whether it was longer.
func cut(s string, most int) (string, bool) {
	if len(s) <= most {
		return s, false
	}
	return strings.ToValidUTF8(s[:most], ""), true
}

// Header is one header of a message, as it reads.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Part is a file that came with a message.
type Part struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id,omitempty"`
	Size        int    `json:"size"`
}

// Read is a message as it reads: what its headers say and its bodies,
// with the files that came with it named. The message as it came is the
// record; this is a reading of it, and never fails.
type Read struct {
	Subject   string    `json:"subject"`
	From      string    `json:"from"`
	To        []string  `json:"to,omitempty"`
	Cc        []string  `json:"cc,omitempty"`
	ReplyTo   string    `json:"reply_to,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Date      time.Time `json:"date,omitzero"`
	Text      string    `json:"text,omitempty"`
	HTML      string    `json:"html,omitempty"`
	Headers   []Header  `json:"headers,omitempty"`
	// Attachments are its files, in the order they came: a file's place
	// counts from 1.
	Attachments []Part `json:"attachments,omitempty"`
	// Partial says some of it could not be read as it was written: a
	// charset nobody knows, or a part that is broken. What is
	// here may then be incomplete or marked; the message as it came
	// holds all of it.
	Partial bool `json:"partial,omitempty"`
}

// filed is a part as a file: named by its place where it has no name.
func filed(l leaf, place int) Part {
	name := l.name
	if name == "" {
		name = fmt.Sprintf("file-%d", place)
	}
	return Part{Filename: name, ContentType: l.kind, ContentID: l.id, Size: len(l.content)}
}

// addresses are a header's addresses, each without its name.
func addresses(h mail.Header, name string) []string {
	list, err := h.AddressList(name)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out
}

// Parse reads a message. The first plain text and the first HTML that
// are not sent as files are its bodies; every other part is a file.
func Parse(raw []byte) Read {
	var out Read
	h, whole := walk(raw, func(l leaf) {
		body := &out.Text
		if l.kind == "text/html" {
			body = &out.HTML
		}
		if l.file || *body != "" || len(l.content) == 0 {
			if l.file || len(l.content) > 0 {
				out.Attachments = append(out.Attachments, filed(l, len(out.Attachments)+1))
			}
			return
		}
		// Cut before it is read as text: a charset can make it longer.
		content, long := l.content, len(l.content) > maxBody
		if long {
			content = content[:maxBody]
		}
		read, ok := text(content, l.params["charset"])
		if *body, _ = cut(read, maxBody); !ok || long || len(read) > maxBody {
			out.Partial = true
		}
	})
	out.Partial = out.Partial || !whole
	if h == nil {
		return out
	}
	out.Subject, out.From = header(h.Get("Subject")), header(h.Get("From"))
	out.ReplyTo, out.MessageID = header(h.Get("Reply-To")), h.Get("Message-Id")
	out.To, out.Cc = addresses(h, "To"), addresses(h, "Cc")
	out.Date, _ = h.Date()
	out.Date = out.Date.UTC()
	// By name, the provider's verdicts first: the same every time it
	// is read, whatever else a sender wrote.
	names := slices.Sorted(maps.Keys(h))
	after := func(name string) int {
		if slices.Contains(verdicts, name) {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(after(a), after(b)) })
	for _, name := range names {
		for _, v := range h[name] {
			if len(out.Headers) == maxHeaders {
				out.Partial = true
				return out
			}
			value, long := cut(header(v), maxValue)
			out.Partial = out.Partial || long
			out.Headers = append(out.Headers, Header{name, value})
		}
	}
	return out
}

// File answers the file at a place of a message, from 1 as Parse lists
// them, with what it holds; false for none.
func File(raw []byte, place int) (Part, []byte, bool) {
	var (
		found   Part
		content []byte
		at      int
		text    bool
		html    bool
	)
	walk(raw, func(l leaf) {
		// As Parse: the first text and the first HTML are bodies.
		body := &text
		if l.kind == "text/html" {
			body = &html
		}
		if !l.file && !*body && len(l.content) > 0 {
			*body = true
			return
		}
		if !l.file && len(l.content) == 0 {
			return
		}
		if at++; at == place {
			found, content = filed(l, at), l.content
		}
	})
	return found, content, at >= place && place >= 1
}
