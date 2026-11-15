package download_test

import (
	"mime"
	"net/http/httptest"
	"testing"

	"github.com/mettjs/cairnpost/internal/download"
)

// Whatever a file is called and holds, it is answered as bytes to save:
// a page is not shown, a script is not run, and its name comes back as
// it was.
func TestAFileIsOnlyEverSaved(t *testing.T) {
	for _, name := range []string{"page.html", "übersicht 2030.svg", "a\"b.js", "x\r\ny.html", ""} {
		rec := httptest.NewRecorder()
		download.Serve(rec, name, []byte("<script>alert(1)</script>"))
		h := rec.Header()
		kind, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
		if err != nil || kind != "attachment" || h.Get("Content-Type") != "application/octet-stream" ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'; frame-ancestors 'none'" ||
			h.Get("Cache-Control") != "no-store" || rec.Body.String() != "<script>alert(1)</script>" {
			t.Errorf("%q: %v %v", name, h, err)
		}
		if got := params["filename"]; got != name {
			t.Errorf("%q came back as %q", name, got)
		}
	}
}

func TestAPlaceIsDigitsFromOne(t *testing.T) {
	for s, want := range map[string]int{"1": 1, "10": 10, "999999": 999999, "": 0, "0": 0, "01": 0, "+1": 0, "-1": 0, "1a": 0,
		" 1": 0, "1.0": 0, "1000000": 0, "99999999999999999999": 0, "first": 0} {
		if got := download.Place(s); got != want {
			t.Errorf("%q is place %d", s, got)
		}
	}
}
