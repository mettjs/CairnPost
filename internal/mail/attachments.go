package mail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Attachment is a file a caller asks to be sent with an email.
type Attachment struct {
	// Filename is the name its reader sees.
	Filename string
	// Type is its media type ("application/pdf"); "" has it told from
	// the filename.
	Type string
	// ContentID, when set, makes it part of the HTML body, which shows
	// it with "cid:" and this name; "" is a file beside the email.
	ContentID string
	Content   []byte
}

// What an attachment is held to whatever the upstream. A name and a
// type are written into headers by whoever delivers.
const (
	maxFilename  = 255
	maxMediaType = 255
	maxContentID = 128
)

// contentID is a name an HTML body can show an attachment by: words
// with dots between, and at most one "@", as the header that carries it
// wants.
var contentID = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*(@[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*)?$`)

// attachments reads a request's files into the form a driver takes. An
// error names a file by its place, never by its name: a name is an
// email's words.
//
// inline says whether the driver can send a file the HTML shows.
func attachments(r Request, most int, mostBytes int64, inline bool) ([]driver.Attachment, error) {
	if len(r.Attachments) > most {
		return nil, &Invalid{"attachments", fmt.Sprintf(`An email has at most %d attachments; this one has %d.`, most, len(r.Attachments))}
	}
	var (
		out   []driver.Attachment
		total int64
		ids   = map[string]bool{}
	)
	for i, a := range r.Attachments {
		bad := func(reason string) error {
			return &Invalid{"attachments", fmt.Sprintf("Attachment %d: %s", i+1, reason)}
		}
		switch {
		case !fileName(a.Filename):
			return nil, bad(`"filename" must be a file's name of at most 255 bytes: no path, no quote, and no character that cannot be seen.`)
		case len(a.Content) == 0:
			return nil, bad(`"content" must not be empty.`)
		case a.ContentID != "" && (len(a.ContentID) > maxContentID || !contentID.MatchString(a.ContentID)):
			return nil, bad(`"content_id" must be at most 128 letters, digits, "_" and "-", with single dots between and at most one "@".`)
		case a.ContentID != "" && !inline:
			return nil, bad(`"content_id" is for a file the "html" body shows, which this server's mail provider cannot send. Leave it out to send the file beside the email.`)
		case a.ContentID != "" && ids[a.ContentID]:
			return nil, bad(`its "content_id" is another attachment's too; give each its own.`)
		case a.ContentID != "" && !strings.Contains(r.HTML, "cid:"+a.ContentID):
			return nil, bad(`"content_id" is for a file the "html" body shows, and the "html" does not name it ("cid:" and the id).`)
		}
		kind, ok := fileType(a.Type, a.Filename)
		if !ok || len(kind) > maxMediaType {
			return nil, bad(`"content_type" must be a media type, such as "application/pdf", with no parameter but a charset.`)
		}
		if total += int64(len(a.Content)); total > mostBytes {
			return nil, &Invalid{"attachments", fmt.Sprintf(`An email's attachments may be %d bytes in all; these are more.`, mostBytes)}
		}
		ids[a.ContentID] = true
		out = append(out, driver.Attachment{Filename: a.Filename, Type: kind, ContentID: a.ContentID, Content: a.Content})
	}
	return out, nil
}

// fingerprint is a digest of a request keyed by the caller's API key, which
// is kept nowhere: whoever reads the digest cannot test a guessed body
// against it.
func fingerprint(presented string, m driver.Message, at time.Time, was *asked) []byte {
	files := m.Attachments
	m.Attachments = nil
	if was != nil {
		// A request that names a template is compared as it was asked,
		// not by what the template said that day: the same request made
		// again after the template changed is still the same request.
		m.Subject, m.HTML, m.Text = was.subject, "", ""
	}
	// encoding/json writes a struct's fields in order and a map's keys
	// sorted, so equal requests encode equally.
	raw, _ := json.Marshal(m)
	mac := hmac.New(sha256.New, []byte(presented))
	mac.Write(raw)
	// A file's bytes go in as they are, each part behind its length, so
	// no two sets of files read the same.
	for _, f := range files {
		for _, part := range [][]byte{[]byte(f.Filename), []byte(f.Type), []byte(f.ContentID), f.Content} {
			mac.Write(binary.BigEndian.AppendUint64(nil, uint64(len(part))))
			mac.Write(part)
		}
	}
	// When it is to be sent is part of what was asked: one part where
	// files come in fours, so it reads as no file.
	if !at.IsZero() {
		when := at.UTC().AppendFormat(nil, time.RFC3339Nano)
		mac.Write(binary.BigEndian.AppendUint64(nil, uint64(len(when))))
		mac.Write(when)
	}
	if was != nil {
		was.write(mac)
	}
	return mac.Sum(nil)
}
