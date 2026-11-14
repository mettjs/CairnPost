package message

import (
	"bytes"
	"encoding/base64"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"

	"github.com/mettjs/cairnpost/internal/driver"
)

// part is one piece of a message: words, a file, or several pieces
// that belong together.
type part struct {
	// kind is its media type.
	kind string
	// headers are what it has beside its type, in order.
	headers [][2]string
	// text is written as quoted-printable, file as base64; one that
	// has kids has neither.
	text []byte
	file []byte
	kids []part
}

// whole is a message's content as parts within parts:
//
//	mixed: the email, then each file beside it
//	  alternative: the text, then the HTML, for a reader to choose
//	    related: the HTML, then each file it shows
//
// A level with a single piece is that piece.
func whole(m driver.Message) part {
	var shown, beside []part
	for _, a := range m.Attachments {
		if a.ContentID != "" {
			shown = append(shown, filePart(a, "inline"))
		} else {
			beside = append(beside, filePart(a, "attachment"))
		}
	}
	var bodies []part
	if m.Text != "" {
		bodies = append(bodies, part{kind: "text/plain; charset=utf-8", text: []byte(m.Text)})
	}
	if m.HTML != "" {
		html := part{kind: "text/html; charset=utf-8", text: []byte(m.HTML)}
		bodies = append(bodies, group("multipart/related", append([]part{html}, shown...)))
	}
	return group("multipart/mixed", append([]part{group("multipart/alternative", bodies)}, beside...))
}

// group is several parts as one, or the one alone.
func group(kind string, kids []part) part {
	if len(kids) == 1 {
		return kids[0]
	}
	return part{kind: kind, kids: kids}
}

// filePart is a file as a part: beside the email ("attachment") or
// shown by its HTML ("inline"). A name a header cannot carry as it is
// is written in the encoded form readers know.
func filePart(a driver.Attachment, how string) part {
	p := part{kind: a.Type, file: a.Content}
	if d := mime.FormatMediaType(how, map[string]string{"filename": a.Filename}); d != "" {
		p.headers = append(p.headers, [2]string{"Content-Disposition", d})
	} else {
		p.headers = append(p.headers, [2]string{"Content-Disposition", how})
	}
	if a.ContentID != "" {
		p.headers = append(p.headers, [2]string{"Content-ID", "<" + a.ContentID + ">"})
	}
	return p
}

// write writes a part: its headers, an empty line, its content.
func (p part) write(out *bytes.Buffer, boundary func() string) error {
	if len(p.kids) > 0 {
		w := multipart.NewWriter(out)
		if err := w.SetBoundary(boundary()); err != nil {
			return err
		}
		field(out, "Content-Type", mime.FormatMediaType(p.kind, map[string]string{"boundary": w.Boundary()}))
		out.WriteString("\r\n")
		for _, kid := range p.kids {
			// The kid writes its own headers: the writer is asked for
			// the line between parts alone.
			if _, err := w.CreatePart(textproto.MIMEHeader{}); err != nil {
				return err
			}
			// CreatePart ends an empty header with an empty line; the
			// kid's headers go in its place.
			out.Truncate(out.Len() - 2)
			if err := kid.write(out, boundary); err != nil {
				return err
			}
		}
		return w.Close()
	}
	field(out, "Content-Type", p.kind)
	for _, h := range p.headers {
		field(out, h[0], h[1])
	}
	if p.file != nil {
		field(out, "Content-Transfer-Encoding", "base64")
		out.WriteString("\r\n")
		lines(out, p.file)
		return nil
	}
	field(out, "Content-Transfer-Encoding", "quoted-printable")
	out.WriteString("\r\n")
	q := quotedprintable.NewWriter(out)
	if _, err := q.Write(p.text); err != nil {
		return err
	}
	return q.Close()
}

// lines writes bytes as base64, in lines of the length mail takes.
func lines(out *bytes.Buffer, raw []byte) {
	const perLine = 57 // 76 characters of base64
	for len(raw) > 0 {
		n := min(len(raw), perLine)
		out.WriteString(base64.StdEncoding.EncodeToString(raw[:n]))
		out.WriteString("\r\n")
		raw = raw[n:]
	}
}
