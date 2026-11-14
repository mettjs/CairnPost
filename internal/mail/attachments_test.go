package mail_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/mail"
)

func pdf(name string, content string) mail.Attachment {
	return mail.Attachment{Filename: name, Content: []byte(content)}
}

// A file goes to the driver as it came, its type told from its name
// when the caller gave none; and it is part of what a key remembers.
func TestAnAttachmentIsSentAndIsPartOfTheRequest(t *testing.T) {
	r := newRig(t)
	req := request("k-file")
	req.HTML = "<img src=cid:logo>"
	req.Attachments = []mail.Attachment{pdf("invoice.pdf", "%PDF-1"), {Filename: "logo.bin", Type: "image/png", ContentID: "logo", Content: []byte{0, 1, 2}}}
	id, err := r.send(req)
	if err != nil {
		t.Fatal(err)
	}
	got := r.drv.last.Attachments
	if len(got) != 2 || got[0].Filename != "invoice.pdf" || got[0].Type != "application/pdf" || string(got[0].Content) != "%PDF-1" ||
		got[1].Type != "image/png" || got[1].ContentID != "logo" || !bytes.Equal(got[1].Content, []byte{0, 1, 2}) {
		t.Fatalf("the driver was handed %+v", got)
	}
	// The same request is the same email; another file under the key is
	// another request.
	if again, err := r.send(req); err != nil || again != id || r.drv.sends != 1 {
		t.Fatalf("a replay: %q %v, %d sends", again, err, r.drv.sends)
	}
	req.Attachments[0] = pdf("invoice.pdf", "%PDF-2")
	if _, err := r.send(req); !errors.Is(err, mail.ErrMismatch) {
		t.Fatalf("with another file under the key: %v", err)
	}
	// A name with no known ending is sent as bytes of no kind.
	plain := request("")
	plain.Attachments = []mail.Attachment{pdf("notes", "x")}
	if _, err := r.send(plain); err != nil || r.drv.last.Attachments[0].Type != "application/octet-stream" {
		t.Fatalf("%v %+v", err, r.drv.last.Attachments)
	}
}

// What cannot be sent is refused before anything is, by the file's
// place: its name is the email's words and is never said back.
func TestAttachmentsThatCanNeverBeSent(t *testing.T) {
	cases := map[string]func(*mail.Request){
		"a path":        func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("NAMEMARK/x.pdf", "x")} },
		"a line break":  func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("NAMEMARK\r\nBcc: x", "x")} },
		"no name":       func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("", "x")} },
		"a long name":   func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf(strings.Repeat("n", 256), "x")} },
		"nothing in it": func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("NAMEMARK.pdf", "")} },
		"a type that is none": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", Type: "not a type", Content: []byte("x")}}
		},
		"a type of two lines": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", Type: "text/plain\r\nX: y", Content: []byte("x")}}
		},
		"a content id of spaces": func(r *mail.Request) {
			r.HTML = "<p>"
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", ContentID: "two words", Content: []byte("x")}}
		},
		"a content id twice": func(r *mail.Request) {
			r.HTML = "<p>"
			r.Attachments = []mail.Attachment{{Filename: "a", ContentID: "one", Content: []byte("x")}, {Filename: "NAMEMARK", ContentID: "one", Content: []byte("x")}}
		},
		"a content id with no html": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", ContentID: "one", Content: []byte("x")}}
		},
		"too many": func(r *mail.Request) {
			for range mail.DefaultMaxAttachments + 1 {
				r.Attachments = append(r.Attachments, pdf("NAMEMARK.pdf", "x"))
			}
		},
		"too large": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{pdf("a.pdf", "x"), {Filename: "NAMEMARK", Content: make([]byte, mail.DefaultMaxAttachmentBytes)}}
		},
	}
	for name, breakIt := range cases {
		r := newRig(t)
		req := request("")
		breakIt(&req)
		var invalid *mail.Invalid
		if _, err := r.send(req); !errors.As(err, &invalid) || invalid.Field != "attachments" || r.drv.sends != 0 {
			t.Errorf("%s: err %v, sends %d", name, err, r.drv.sends)
		} else if strings.Contains(err.Error(), "NAMEMARK") {
			t.Errorf("%s: the refusal says the file's name: %v", name, err)
		}
	}
	// Exactly as large as allowed is taken.
	r := newRig(t)
	req := request("")
	req.Attachments = []mail.Attachment{{Filename: "full.bin", Content: make([]byte, mail.DefaultMaxAttachmentBytes)}}
	if _, err := r.send(req); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
}
