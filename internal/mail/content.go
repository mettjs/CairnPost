package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Words is what an email said, as it is kept.
type Words struct {
	FromName string            `json:"from_name,omitempty"`
	ReplyTo  string            `json:"reply_to,omitempty"`
	Subject  string            `json:"subject"`
	HTML     string            `json:"html,omitempty"`
	Text     string            `json:"text,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	// Attachments say what files went with it: a name is the email's
	// words too. A file's bytes are never kept.
	Attachments []File `json:"attachments,omitempty"`
}

// File is what an attachment was, without its bytes.
type File struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	// ContentID is set for a file the HTML body shows.
	ContentID string `json:"content_id,omitempty"`
	Size      int    `json:"size"`
}

// filesOf says what a message's attachments were.
func filesOf(m driver.Message) []File {
	var out []File
	for _, a := range m.Attachments {
		out = append(out, File{Filename: a.Filename, ContentType: a.Type, ContentID: a.ContentID, Size: len(a.Content)})
	}
	return out
}

// Content is what an email said, as it is shown. It is kept only when the
// operator chose to keep it, for the time they chose, and is never logged.
type Content struct {
	Words
	// KeptUntil is when it stops being shown, and is deleted soon after.
	KeptUntil time.Time `json:"kept_until"`
}

// Details answers what receiving servers said of a message's events, by
// event id, while that is kept; none when content is not kept at all.
func (s *Service) Details(ctx context.Context, id string) (map[string]string, error) {
	if s.opt.KeepContent == 0 {
		return nil, nil
	}
	return s.events.Details(ctx, id, s.opt.KeepContent)
}

// contentOf answers a message's words as they are kept; nil when they are
// not. The answer is made only if asked for: a request that turns out to
// be a repeat, or refused, never pays for it.
func (s *Service) contentOf(m driver.Message) func() []byte {
	if s.opt.KeepContent == 0 {
		return nil
	}
	// Said now: what is kept must not hold on to the files' bytes.
	files := filesOf(m)
	m.Attachments = nil
	return func() []byte {
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false) // a body is kept as it was written
		// Strings and a map of strings always encode.
		_ = enc.Encode(Words{FromName: m.FromName, ReplyTo: m.ReplyTo, Subject: m.Subject,
			HTML: m.HTML, Text: m.Text, Headers: m.Headers, Attachments: files})
		return out.Bytes()
	}
}

// Content answers what a message said, while that is kept; nil when it
// never was, is past its time, or content is not kept at all. Turning the
// setting off hides at once what an earlier setting kept.
//
// What an email that waits to be sent says is kept until it is sent,
// whatever the setting, and is answered here too.
func (s *Service) Content(ctx context.Context, id string) (*Content, error) {
	if held, err := s.heldContent(ctx, id); held != nil || err != nil {
		return held, err
	}
	if s.opt.KeepContent == 0 {
		return nil, nil
	}
	raw, until, err := s.store.Content(ctx, id, s.opt.KeepContent)
	if err != nil || raw == nil {
		return nil, err
	}
	var c Content
	if json.Unmarshal(raw, &c.Words) != nil {
		// The decoder's own error can quote what it choked on.
		return nil, errors.New("mail: a message's kept content is not what was written")
	}
	c.KeptUntil = until.UTC()
	return &c, nil
}
