package message

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

func stamp() Stamp {
	n := 0
	return Stamp{At: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), ID: "abc@example.com",
		Boundary: func() string { n++; return fmt.Sprintf("line-%d", n) }}
}

// leaf is a part as a reader finds it.
type leaf struct {
	kind, disposition, id string
	body                  []byte
}

// read reads a message's parts back as a mail program would, each
// with its content decoded.
func read(t *testing.T, header func(string) string, body io.Reader) []leaf {
	t.Helper()
	kind, params, err := mime.ParseMediaType(header("Content-Type"))
	if err != nil {
		t.Fatalf("the type %q: %v", header("Content-Type"), err)
	}
	if strings.HasPrefix(kind, "multipart/") {
		var out []leaf
		r := multipart.NewReader(body, params["boundary"])
		for {
			p, err := r.NextRawPart()
			if err == io.EOF {
				return append([]leaf{{kind: kind}}, out...)
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, read(t, p.Header.Get, p)...)
		}
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return []leaf{{kind: header("Content-Type"), disposition: header("Content-Disposition"), id: header("Content-ID"),
		body: decode(t, header("Content-Transfer-Encoding"), raw)}}
}

var full = driver.Message{
	From: "app@example.com", FromName: "José, the App", To: []string{"ana@example.org", "bo@example.org"},
	Cc: []string{"cy@example.org"}, Bcc: []string{"hidden@example.org"}, ReplyTo: "help@example.com",
	Subject: "Grüße: your order №7", Text: "Hello,\nline two = done\r\n", HTML: "<p>Hällo <img src=cid:logo></p>",
	Headers: map[string]string{"X-Trace": "t-1", "X-Note": "naïve"},
	Attachments: []driver.Attachment{
		{Filename: "logo.png", Type: "image/png", ContentID: "logo", Content: []byte{0, 1, 2, 250, 251}},
		{Filename: "Rechnung №7.pdf", Type: "application/pdf", Content: bytes.Repeat([]byte("%PDF"), 100)},
	},
}

// A message read back as a mail program reads it says what was
// written: every header, every body and every file, in their places.
func TestAMessageReadsBackAsItWasWritten(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, full, stamp()); err != nil {
		t.Fatal(err)
	}
	raw := out.String()
	if strings.Contains(raw, "hidden@example.org") || strings.Contains(strings.ToLower(raw), "bcc") {
		t.Fatal("a hidden copy's address is in the message")
	}
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.ContainsAny(line, "\r\n") || len(line) > 78 {
			t.Fatalf("a line a server may refuse: %q", line)
		}
	}
	msg, err := netmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	word := new(mime.WordDecoder)
	head := func(name string) string {
		v, err := word.DecodeHeader(msg.Header.Get(name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return v
	}
	from, err := msg.Header.AddressList("From")
	to, _ := msg.Header.AddressList("To")
	date, _ := msg.Header.Date()
	if err != nil || len(from) != 1 || from[0].Name != "José, the App" || from[0].Address != "app@example.com" ||
		len(to) != 2 || to[1].Address != "bo@example.org" || head("Cc") != "cy@example.org" || head("Reply-To") != "help@example.com" ||
		head("Subject") != full.Subject || head("Message-ID") != "<abc@example.com>" || head("MIME-Version") != "1.0" ||
		head("X-Trace") != "t-1" || head("X-Note") != "naïve" || !date.Equal(stamp().At) {
		t.Fatalf("the headers: %v %v", msg.Header, err)
	}
	got := read(t, msg.Header.Get, msg.Body)
	want := []leaf{
		{kind: "multipart/mixed"}, {kind: "multipart/alternative"},
		{kind: "text/plain; charset=utf-8", body: []byte("Hello,\r\nline two = done\r\n")},
		{kind: "multipart/related"},
		{kind: "text/html; charset=utf-8", body: []byte(full.HTML)},
		{kind: "image/png", disposition: "inline; filename=logo.png", id: "<logo>", body: []byte{0, 1, 2, 250, 251}},
		{kind: "application/pdf", body: bytes.Repeat([]byte("%PDF"), 100)},
	}
	if len(got) != len(want) {
		t.Fatalf("%d parts, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.kind != w.kind || !bytes.Equal(g.body, w.body) || g.id != w.id || (w.disposition != "" && g.disposition != w.disposition) {
			t.Errorf("part %d: %q %q %q %q", i, g.kind, g.disposition, g.id, g.body)
		}
	}
	// A name a header cannot carry as it is reads back whole.
	if _, params, err := mime.ParseMediaType(got[6].disposition); err != nil || params["filename"] != "Rechnung №7.pdf" {
		t.Fatalf("the file's name: %q %v", got[6].disposition, err)
	}
}

// With one body and no file a message is that body alone, and the
// same message is the same bytes.
func TestAPlainMessageIsOnePart(t *testing.T) {
	m := driver.Message{From: "app@example.com", To: []string{"ana@example.org"}, Subject: "Hi", Text: "Just text"}
	var one, two bytes.Buffer
	if err := Write(&one, m, stamp()); err != nil {
		t.Fatal(err)
	}
	m.Headers = map[string]string{"B": "2", "A": "1"}
	Write(&two, m, stamp())
	again := bytes.Buffer{}
	Write(&again, m, stamp())
	msg, _ := netmail.ReadMessage(bytes.NewReader(one.Bytes()))
	if got := read(t, msg.Header.Get, msg.Body); len(got) != 1 || got[0].kind != "text/plain; charset=utf-8" || string(got[0].body) != "Just text" {
		t.Fatalf("%+v", got)
	}
	if !bytes.Equal(two.Bytes(), again.Bytes()) || strings.Index(two.String(), "A: 1") > strings.Index(two.String(), "B: 2") {
		t.Fatal("the same message was written two ways")
	}
	// Many addresses and a long subject are folded, not cut.
	var many []string
	for i := range 50 {
		many = append(many, fmt.Sprintf("someone-%d@example.org", i))
	}
	m.To, m.Subject = many, strings.Repeat("word ", 190)+"end"
	var long bytes.Buffer
	Write(&long, m, stamp())
	msg, err := netmail.ReadMessage(bytes.NewReader(long.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if to, err := msg.Header.AddressList("To"); err != nil || len(to) != 50 || msg.Header.Get("Subject") != m.Subject {
		t.Fatalf("%d addresses %v, subject %d bytes", len(to), err, len(msg.Header.Get("Subject")))
	}
}
