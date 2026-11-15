// Package download answers a file as something to save, never something
// to open. What an email's file is called and what it holds are a
// stranger's: served from this site, a page or a picture among them
// would run as this site. So whatever it is, a browser is told to save
// it, to guess nothing about it, and to run nothing in it.
package download

import (
	"mime"
	"net/http"
	"strconv"
)

// Serve writes content as a file to save under name.
func Serve(w http.ResponseWriter, name string, content []byte) {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if disposition == "" {
		// A name no header can carry: saved under the browser's own.
		disposition = "attachment"
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", disposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// Place reads a file's place in an address: a whole number from 1,
// written as digits alone. 0 for anything else, which is no place.
func Place(s string) int {
	if s == "" || len(s) > 6 || s[0] == '0' {
		return 0
	}
	n := 0
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}
