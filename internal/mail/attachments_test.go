package mail_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
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
		"a quote in the name":         func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf(`NAMEMARK".pdf"; x="y`, "x")} },
		"a name of dots":              func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("..", "x")} },
		"a name that ends in a space": func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("NAMEMARK.pdf ", "x")} },
		"a name turned round":         func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("NAMEMARK\u202efdp.exe", "x")} },
		"a line separator":            func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("NAMEMARK\u2028b", "x")} },
		"an unseen character":         func(r *mail.Request) { r.Attachments = []mail.Attachment{pdf("NAMEMARK\u200b.pdf", "x")} },
		"a type with no slash": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", Type: "attachment", Content: []byte("x")}}
		},
		"a type of many parts": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", Type: "multipart/mixed; boundary=x", Content: []byte("x")}}
		},
		"a type that is an email": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", Type: "message/rfc822", Content: []byte("x")}}
		},
		"a type with a second name": func(r *mail.Request) {
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", Type: `text/plain; charset="x"; name="evil.exe"`, Content: []byte("x")}}
		},
		"a content id of two ats": func(r *mail.Request) {
			r.HTML = "cid:a@@b"
			r.Attachments = []mail.Attachment{{Filename: "NAMEMARK", ContentID: "a@@b", Content: []byte("x")}}
		},
		"a content id the html does not name": func(r *mail.Request) {
			r.HTML = "<p>cid:other</p>"
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

// plain is a driver that cannot send a file the HTML shows.
type plain struct{ *fakeDriver }

func (plain) NoInlineFiles() bool { return true }

// What the driver cannot send is refused before anything is recorded,
// in words that say what to change; and a type is sent written anew.
func TestWhatTheProviderCannotSendIsRefusedFirst(t *testing.T) {
	r := newRig(t)
	r.svc = mail.New(r.store, plain{r.drv}, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
		InDoubtWindow: 2 * time.Minute, MaxAttachments: -1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := request("")
	req.Attachments = []mail.Attachment{pdf("a.pdf", "x")}
	var invalid *mail.Invalid
	if _, err := r.send(req); !errors.As(err, &invalid) || r.drv.sends != 0 {
		t.Fatalf("with files off: %v", err)
	}
	r.svc = mail.New(r.store, plain{r.drv}, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
		InDoubtWindow: 2 * time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req.HTML = "<img src=cid:logo>"
	req.Attachments = []mail.Attachment{{Filename: "logo.png", ContentID: "logo", Content: []byte("x")}}
	_, err := r.send(req)
	list, _ := r.store.Messages(context.Background(), store.MessageQuery{Limit: 10})
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Reason, "mail provider cannot send") || r.drv.sends != 0 || len(list) != 0 {
		t.Fatalf("a file the html shows: %v, %d sends, %d on record", err, r.drv.sends, len(list))
	}
	req.Attachments = []mail.Attachment{{Filename: "a.txt", Type: " Text/Plain ; charset=UTF-8", Content: []byte("x")}}
	if _, err := r.send(req); err != nil || r.drv.last.Attachments[0].Type != "text/plain; charset=UTF-8" {
		t.Fatalf("%v %+v", err, r.drv.last.Attachments)
	}
}
