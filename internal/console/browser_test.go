package console_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"

	"github.com/mettjs/cairnpost/internal/console"
)

// cookieOf is a cookie an answer set, nil for none.
func cookieOf(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// browser keeps cookies between requests, as a browser does. With secure
// set to false it drops the ones marked Secure, as one on plain http does.
type browser struct {
	r      *rig
	secure bool
	jar    map[string]string
}

func (b *browser) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	b.r.t.Helper()
	var body = strings.NewReader("")
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, console.Root+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for name, value := range b.jar {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	b.r.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		switch {
		case c.MaxAge < 0:
			delete(b.jar, c.Name)
		case c.Secure && !b.secure:
		default:
			b.jar[c.Name] = c.Value
		}
	}
	return rec
}

// follow makes a request and the one it is redirected to.
func (b *browser) follow(method, path string, form url.Values) *httptest.ResponseRecorder {
	b.r.t.Helper()
	rec := b.do(method, path, form)
	for hops := 0; rec.Code == http.StatusSeeOther && hops < 5; hops++ {
		rec = b.do("GET", strings.TrimPrefix(rec.Header().Get("Location"), console.Root), nil)
	}
	return rec
}

// handled is what a list's address looks like once it is narrowed to an
// address: it carries a handle, and nothing of the address.
var handled = regexp.MustCompile(`^/console/(emails|suppressions)\?(domain=[a-z.]+&)?for=([0-9a-f]{16})(&state=[a-z]+)?$`)

// narrow sends a form that looks for an address and answers the page it
// leads to, without the console's root, and the handle in it.
func (b *browser) narrow(path string, form url.Values) (page, handle string) {
	b.r.t.Helper()
	rec := b.do("POST", path, form)
	at := rec.Header().Get("Location")
	m := handled.FindStringSubmatch(at)
	if rec.Code != http.StatusSeeOther || m == nil || len(rec.Result().Cookies()) != 0 {
		b.r.t.Fatalf("looking for %q: %d %s, cookies %v", form.Get("to"), rec.Code, at, rec.Result().Cookies())
	}
	return strings.TrimPrefix(at, console.Root), m[3]
}
