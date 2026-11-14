// Package capture is the driver for local work and tests: it keeps the most
// recent emails in memory and sends nothing. It is the one place an email's
// subject and body can be read back, which is its whole purpose; nothing it
// holds reaches the database or a log, and it is gone when the process ends.
package capture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Name is the driver's name.
const Name = "capture"

// Captured is an email as it was handed over, but for its files: of
// those it holds what each was, not its bytes.
type Captured struct {
	ID string
	At time.Time
	driver.Message
	Files []File
}

// File is what an attachment was.
type File struct {
	Filename, Type, ContentID string
	Size                      int
}

// Driver keeps the last keep emails.
type Driver struct {
	mu   sync.Mutex
	keep int
	held []Captured
	now  func() time.Time
}

// New returns a driver holding at most keep emails.
func New(keep int, now func() time.Time) *Driver {
	if keep < 1 {
		keep = 1
	}
	return &Driver{keep: keep, now: now}
}

var _ driver.Driver = (*Driver)(nil)

// Name implements driver.Driver.
func (*Driver) Name() string { return Name }

// Send keeps the message and answers an id of its own making.
func (d *Driver) Send(_ context.Context, m driver.Message) (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := "capture-" + hex.EncodeToString(b)
	d.mu.Lock()
	defer d.mu.Unlock()
	c := Captured{ID: id, At: d.now(), Message: m}
	for _, a := range m.Attachments {
		c.Files = append(c.Files, File{Filename: a.Filename, Type: a.Type, ContentID: a.ContentID, Size: len(a.Content)})
	}
	c.Attachments = nil // a file's bytes are let go
	d.held = append(d.held, c)
	if len(d.held) > d.keep {
		d.held = d.held[len(d.held)-d.keep:]
	}
	return id, nil
}

// Held is every email kept, newest first.
func (d *Driver) Held() []Captured {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Captured, len(d.held))
	for i, c := range d.held {
		out[len(d.held)-1-i] = c
	}
	return out
}
