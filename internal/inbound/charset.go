package inbound

import (
	"bytes"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
)

// cp1252 is what the bytes 0x80 to 0x9f are in windows-1252, where it
// differs from Latin-1; 0 for a byte it does not use.
var cp1252 = [32]rune{0x20ac, 0, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021, 0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017d, 0,
	0, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014, 0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0, 0x017e, 0x0178}

// text reads bytes written in a charset as text. ok is false for a
// charset nobody knows, or bytes that are not what it says: the
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
	// Any other charset mail is written in: read by its name, as
	// browsers know it. What its bytes cannot be is marked.
	// A mark for what cannot be read is all a decoder says of it, so
	// text that holds that mark itself is said to be partial too.
	if enc := decoderOf(charset); enc != nil {
		if out, err := enc.NewDecoder().Bytes(raw); err == nil {
			return string(out), !bytes.ContainsRune(out, utf8.RuneError)
		}
	}
	// A charset nobody knows: often the bytes are UTF-8 all the same.
	return strings.ToValidUTF8(string(raw), "�"), false
}

// decoderOf is how to read a charset by its name, nil for one nobody
// knows or nothing here reads.
func decoderOf(charset string) encoding.Encoding {
	switch name := strings.ToLower(strings.TrimSpace(charset)); name {
	case "utf-16", "utf-16be", "utf-16le", "unicode":
		// The text's own mark of its byte order decides, where it has
		// one; without one, plain "utf-16" is big end first.
		order := unicode.BigEndian
		if name == "utf-16le" {
			order = unicode.LittleEndian
		}
		return unicode.UTF16(order, unicode.UseBOM)
	}
	enc, err := htmlindex.Get(strings.TrimSpace(charset))
	if err != nil || enc == encoding.Replacement {
		// Replacement stands for charsets browsers refuse to read
		// (ISO-2022-KR among them): it would answer one mark for all
		// of the text, where the bytes as they are still say something.
		return nil
	}
	return enc
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
