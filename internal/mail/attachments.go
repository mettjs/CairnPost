package mail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"mime"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
)

// contentID is a name an HTML body can show an attachment by.
var contentID = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}$`)

// attachments reads a request's files into the form a driver takes. An
// error names a file by its place, never by its name: a name is an
// email's words.
func attachments(r Request, most int, mostBytes int64) ([]driver.Attachment, error) {
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
		case a.Filename == "" || len(a.Filename) > maxFilename || !utf8.ValidString(a.Filename) ||
			strings.ContainsFunc(a.Filename, unicode.IsControl) || strings.ContainsAny(a.Filename, `/\`):
			return nil, bad(`"filename" must be a file's name of at most 255 bytes, with no path and no control characters.`)
		case len(a.Content) == 0:
			return nil, bad(`"content" must not be empty.`)
		case a.ContentID != "" && !contentID.MatchString(a.ContentID):
			return nil, bad(`"content_id" must be at most 128 letters, digits, ".", "_", "-" or "@".`)
		case a.ContentID != "" && ids[a.ContentID]:
			return nil, bad(`its "content_id" is another attachment's too; give each its own.`)
		case a.ContentID != "" && r.HTML == "":
			return nil, bad(`"content_id" is for a file the "html" body shows, and this email has none.`)
		}
		kind := a.Type
		if kind == "" {
			if kind = mime.TypeByExtension(path.Ext(a.Filename)); kind == "" {
				kind = "application/octet-stream"
			}
		}
		if _, _, err := mime.ParseMediaType(kind); err != nil || len(kind) > maxMediaType || strings.ContainsFunc(kind, unicode.IsControl) {
			return nil, bad(`"content_type" must be a media type, such as "application/pdf".`)
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
func fingerprint(presented string, m driver.Message) []byte {
	files := m.Attachments
	m.Attachments = nil
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
	return mac.Sum(nil)
}
