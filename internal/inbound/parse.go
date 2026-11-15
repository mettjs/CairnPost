package inbound

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a message is read to at most: deeper or more is left unread.
const (
	maxDepth = 10
	maxParts = 500
)

// leaf is one part of a message that holds something, read out of how
// it was written.
type leaf struct {
	kind    string            // its media type, in small letters
	params  map[string]string // of its Content-Type
	name    string            // the file's name, "" for none
	id      string            // its Content-ID, without the brackets
	file    bool              // sent as a file, whatever it holds
	content []byte
}

// reading walks a message's parts.
type reading struct {
	visit func(leaf)
	parts int
	// unread says something of it could not be read as it was written.
	unread bool
}

// walk reads a message and hands visit each part of it that holds
// something, in the order they come. It answers the message's own
// headers, and whether all of it could be read.
func walk(raw []byte, visit func(leaf)) (mail.Header, bool) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	r := &reading{visit: visit}
	r.part(textproto.MIMEHeader(m.Header), m.Body, 0)
	return m.Header, !r.unread
}

func (r *reading) part(h textproto.MIMEHeader, body io.Reader, depth int) {
	if r.parts++; r.parts > maxParts {
		r.unread = true
		return
	}
	kind, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if h.Get("Content-Type") == "" {
		kind, err = "text/plain", nil
	} else if err != nil {
		kind, r.unread = "application/octet-stream", true
	}
	if strings.HasPrefix(kind, "multipart/") && params["boundary"] != "" {
		if depth >= maxDepth {
			r.unread = true
			return
		}
		parts := multipart.NewReader(body, params["boundary"])
		for {
			p, err := parts.NextRawPart()
			if err == io.EOF {
				return
			}
			if err != nil {
				r.unread = true
				return
			}
			r.part(p.Header, p, depth+1)
		}
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding"))) {
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	}
	content, err := io.ReadAll(body)
	if err != nil {
		r.unread = true // what was read before it broke is kept
	}
	l := leaf{kind: kind, params: params, content: content, id: strings.Trim(strings.TrimSpace(h.Get("Content-Id")), "<>")}
	how, given, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	if l.name = given["filename"]; l.name == "" {
		l.name = params["name"]
	}
	l.name = named(l.name)
	l.file = how == "attachment" || l.name != "" || (kind != "text/plain" && kind != "text/html")
	r.visit(l)
}

// maxName is how long a file's name may be.
const maxName = 255

// named is a file's name as it is shown and saved under: its last part,
// of a length, with nothing in it that is not to be seen or that turns
// what follows around. "" for a name that is none.
func named(name string) string {
	name = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || unicode.In(c, unicode.Cf, unicode.Zl, unicode.Zp) || c == '"' || c == utf8.RuneError {
			return -1
		}
		return c
	}, header(name))
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, `\`, "/")))
	if len(name) > maxName {
		name = strings.TrimSpace(strings.ToValidUTF8(name[:maxName], ""))
	}
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return name
}
