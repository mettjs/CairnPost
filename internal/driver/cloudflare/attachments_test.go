package cloudflare

import (
	"context"
	"testing"

	"github.com/mettjs/cairnpost/internal/driver"
)

// A file goes to Cloudflare in base64 with its name and type, as a
// file beside the email; one the HTML shows goes as part of it, under
// its content id.
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
		one["disposition"] != "attachment" || one["content"] != "JVBERi0x" || one["content_id"] != nil {
		t.Fatalf("the request's files: %v", files)
	}
	m.Attachments[0].ContentID = "logo"
	if _, err := d.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	files, _ = s.request["attachments"].([]any)
	if one, _ = files[0].(map[string]any); one["disposition"] != "inline" || one["content_id"] != "logo" {
		t.Fatalf("a file the html shows: %v", one)
	}
	// With no files, the field is not sent.
	if _, err := d.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if _, has := s.request["attachments"]; has {
		t.Fatal("an email with no files was sent with the field")
	}
}
