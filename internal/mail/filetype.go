package mail

import (
	"mime"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// typeOf is the media type a file's name tells, for a caller that gave
// none. It is a list of our own, not the host's: every server tells the
// same type for the same name, so a request repeated on another is the
// same request. A name it does not know is bytes of no kind.
var typeOf = map[string]string{
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".svg": "image/svg+xml", ".txt": "text/plain", ".csv": "text/csv", ".html": "text/html",
	".htm": "text/html", ".json": "application/json", ".xml": "application/xml", ".zip": "application/zip",
	".ics": "text/calendar", ".doc": "application/msword", ".xls": "application/vnd.ms-excel",
	".ppt":  "application/vnd.ms-powerpoint",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".mp3":  "audio/mpeg", ".wav": "audio/wav", ".mp4": "video/mp4",
}

// mediaType is a type and subtype as a header takes them.
var mediaType = regexp.MustCompile(`^[a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+-]+$`)

// charsetName is a charset parameter's value.
var charsetName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

// fileType answers the media type an attachment is sent as: the
// caller's, written anew, or the one its name tells. It is one a part
// of an email may have: no multipart and no message, which are an
// email's own structure, and no parameter but a charset, since any
// other would be written into a header as the caller chose it.
func fileType(given, filename string) (string, bool) {
	if given == "" {
		if known := typeOf[strings.ToLower(path.Ext(filename))]; known != "" {
			return known, true
		}
		return "application/octet-stream", true
	}
	kind, params, err := mime.ParseMediaType(given)
	if err != nil || !mediaType.MatchString(kind) || strings.HasPrefix(kind, "multipart/") || strings.HasPrefix(kind, "message/") {
		return "", false
	}
	charset, has := params["charset"]
	if len(params) > 1 || len(params) == 1 && (!has || !charsetName.MatchString(charset)) {
		return "", false
	}
	return mime.FormatMediaType(kind, params), true
}

// fileName reports whether a name is one a file may be sent under: a
// name, not a path, of one line, that reads as it is written. A quote
// would end the header value it is written into; a character that
// cannot be seen, or that turns the writing round, lets a name show as
// another than it is.
func fileName(name string) bool {
	if name == "" || len(name) > maxFilename || name != strings.TrimSpace(name) || strings.Trim(name, ". ") == "" ||
		strings.ContainsAny(name, `/\"`) || !utf8.ValidString(name) {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == unicode.ReplacementChar
	})
}
