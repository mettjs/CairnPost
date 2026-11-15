package mail

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// A request with no files reads as it did before files could be sent:
// a key in use across an upgrade is still the same request. And a type
// told from a name is told the same on every host.
func TestARequestWithoutFilesKeepsItsFingerprint(t *testing.T) {
	m := driver.Message{From: "app@example.com", FromName: "App", To: []string{"ana@example.org"}, Subject: "s", Text: "t"}
	mac := hmac.New(sha256.New, []byte("cp_key"))
	mac.Write([]byte(`{"From":"app@example.com","FromName":"App","To":["ana@example.org"],"Cc":null,"Bcc":null,` +
		`"ReplyTo":"","Subject":"s","HTML":"","Text":"t","Headers":null}`))
	if !bytes.Equal(fingerprint("cp_key", m, time.Time{}, nil), mac.Sum(nil)) {
		t.Fatal("the fingerprint of a request without files changed")
	}
	m.Attachments = []driver.Attachment{{Filename: "a", Type: "text/plain", Content: []byte("x")}}
	if bytes.Equal(fingerprint("cp_key", m, time.Time{}, nil), mac.Sum(nil)) {
		t.Fatal("a file is no part of the fingerprint")
	}
	for name, want := range map[string]string{"a.PDF": "application/pdf", "notes.txt": "text/plain", "x.unknown": "application/octet-stream", "none": "application/octet-stream"} {
		if got, ok := fileType("", name); !ok || got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
}
