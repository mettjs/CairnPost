package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/mettjs/cairnpost/internal/store"
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
	// words too. What a file held is kept apart, where it is kept.
	Attachments []File `json:"attachments,omitempty"`
}

// File is what an attachment was, without its bytes.
type File struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	// ContentID is set for a file the HTML body shows.
	ContentID string `json:"content_id,omitempty"`
	Size      int    `json:"size"`
	// Kept says what the file held can still be read (FileFor).
	Kept bool `json:"kept,omitempty"`
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
	for i := range files {
		files[i].Kept = s.opt.KeepFiles
	}
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

// keptFiles are a message's files as they are kept with what it said;
// none where what it said is not kept, or its files are not.
func (s *Service) keptFiles(m driver.Message) []store.HeldFile {
	if s.opt.KeepContent == 0 || !s.opt.KeepFiles {
		return nil
	}
	var out []store.HeldFile
	for _, a := range m.Attachments {
		out = append(out, store.HeldFile{Filename: a.Filename, Type: a.Type, ContentID: a.ContentID, Content: a.Content})
	}
	return out
}

// maxPlace is past any place a file can have: no email carries so many.
const maxPlace = 1 << 20

// Attached is a file that went, or is to go, with an email.
type Attached struct {
	File
	Content []byte
}

// FileFor answers, for a key, one of an email's files by its place
// from 1, as Content lists them: while the email waits to be sent, or
// while what it said is kept and its files with it. store.ErrNotFound
// for an email the key may not read, for a key that is not shown what
// emails said, and for a file that is not kept.
func (s *Service) FileFor(ctx context.Context, key store.Key, id string, place int) (*Attached, error) {
	m, _, err := s.MessageFor(ctx, key, id)
	if err != nil {
		return nil, err
	}
	if !ReadsContent(key) || place < 1 {
		return nil, store.ErrNotFound
	}
	var f *store.HeldFile
	// Decided as Content decides: an email is read as one that waits for
	// as long as its words are held, so what Content says is kept is.
	if h, err := s.held.Held(ctx, m.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	} else if err == nil && h.State == store.StateScheduled {
		_, files, err := s.held.HeldWords(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		if place <= len(files) {
			f = &files[place-1]
		}
	} else if s.opt.KeepContent != 0 && s.opt.KeepFiles && place <= maxPlace {
		if f, err = s.store.File(ctx, m.ID, place-1, s.opt.KeepContent); err != nil {
			return nil, err
		}
	}
	if f == nil {
		return nil, store.ErrNotFound
	}
	return &Attached{File{Filename: f.Filename, ContentType: f.Type, ContentID: f.ContentID, Size: len(f.Content)}, f.Content}, nil
}

// Content answers what a message said, while that is kept; nil when it
// never was, is past its time, or content is not kept at all. Turning the
// setting off hides at once what an earlier setting kept, and so does
// turning off the keeping of files for the files.
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
	for i := range c.Attachments {
		c.Attachments[i].Kept = c.Attachments[i].Kept && s.opt.KeepFiles
	}
	return &c, nil
}
