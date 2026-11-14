package message

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"testing"
)

// decode undoes a part's transfer encoding.
func decode(t *testing.T, how string, raw []byte) []byte {
	t.Helper()
	var r io.Reader = bytes.NewReader(raw)
	switch how {
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, bytes.NewReader(bytes.ReplaceAll(raw, []byte("\r\n"), nil)))
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: %v", how, err)
	}
	return out
}
