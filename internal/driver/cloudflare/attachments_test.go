package cloudflare

import (
	"context"
	"errors"
	"testing"

	"github.com/mettjs/cairnpost/internal/driver"
)

// A file goes to Cloudflare in base64 with its name and type, as a
// file beside the email. One the HTML shows is refused, and nothing is
// asked of Cloudflare: its field for that is not known.
func TestAttachmentsGoAsCloudflareTakesThem(t *testing.T) {
	s := &stand{status: 200, body: taken}
	d := s.serve(t)
	m := message
	m.Attachments = []driver.Attachment{{Filename: "invoice.pdf", Type: "application/pdf", Content: []byte("%PDF-1")}}
	if _, err := d.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	files, _ := s.request["attachments"].([]any)
	one, _ := files[0].(map[string]any)
	if len(files) != 1 || one["filename"] != "invoice.pdf" || one["type"] != "application/pdf" ||
		one["disposition"] != "attachment" || one["content"] != "JVBERi0x" {
		t.Fatalf("the request's files: %v", files)
	}
	m.Attachments[0].ContentID = "logo"
	if _, err := d.Send(context.Background(), m); !errors.Is(err, driver.ErrRejected) || s.sends != 1 {
		t.Fatalf("a file the html shows: %v, %d sends", err, s.sends)
	}
	// With no files, the field is not sent.
	if _, err := d.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if _, has := s.request["attachments"]; has {
		t.Fatal("an email with no files was sent with the field")
	}
}
