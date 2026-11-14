package capture

import (
	"context"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Of a file it holds what it was, never its bytes.
func TestAFilesBytesAreLetGo(t *testing.T) {
	d := New(5, time.Now)
	if _, err := d.Send(context.Background(), driver.Message{From: "app@example.com", To: []string{"ana@example.org"}, Subject: "s",
		Attachments: []driver.Attachment{{Filename: "a.pdf", Type: "application/pdf", ContentID: "c", Content: []byte("12345")}}}); err != nil {
		t.Fatal(err)
	}
	held := d.Held()
	if len(held) != 1 || held[0].Attachments != nil || len(held[0].Files) != 1 ||
		held[0].Files[0] != (File{Filename: "a.pdf", Type: "application/pdf", ContentID: "c", Size: 5}) {
		t.Fatalf("%+v", held)
	}
}
