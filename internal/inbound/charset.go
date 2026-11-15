package inbound

import (
	"bytes"
	"io"
	"mime"
	"strings"
	"unicode/utf8"
)

// cp1252 is what the bytes 0x80 to 0x9f are in windows-1252, where it
// differs from Latin-1; 0 for a byte it does not use.
var cp1252 = [32]rune{0x20ac, 0, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021, 0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017d, 0,
	0, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014, 0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0, 0x017e, 0x0178}

// text reads bytes written in a charset as text. ok is false for a
// charset this does not know, or bytes that are not what it says: the
// text is then the bytes as far as they read, the rest marked.
func text(raw []byte, charset string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return strings.ToValidUTF8(string(raw), "�"), utf8.Valid(raw)
	case "iso-8859-1", "latin1", "iso8859-1", "windows-1252", "cp1252":
		// Mail that says Latin-1 is, more often than not, windows-1252:
		// read as that, which is Latin-1 but for those 32 bytes.
		var b strings.Builder
		ok := true
		for _, c := range raw {
			r := rune(c)
			if c >= 0x80 && c <= 0x9f {
				if r = cp1252[c-0x80]; r == 0 {
					r, ok = '�', false
				}
			}
			b.WriteRune(r)
		}
		return b.String(), ok
	}
	// Another charset: often the bytes are UTF-8 all the same.
	return strings.ToValidUTF8(string(raw), "�"), false
}

// words reads a header's value, with the words it writes encoded.
var words = mime.WordDecoder{CharsetReader: func(charset string, in io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	out, _ := text(raw, charset)
	return bytes.NewReader([]byte(out)), nil
}}

// header is a header's value as it reads: as it was written where it
// cannot be read.
func header(value string) string {
	if out, err := words.DecodeHeader(value); err == nil {
		return out
	}
	return strings.ToValidUTF8(value, "�")
}
