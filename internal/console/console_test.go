package console_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/driver/cloudflare"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/session"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
	"github.com/mettjs/cairnpost/internal/suppress"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// rig is a console over a store on a clock the test moves.
type rig struct {
	t    *testing.T
	h    http.Handler
	s    *memstore.Store
	ks   *keys.Service
	mail *mail.Service
	// dns is what the DNS the console looks up holds: text records by
	// name, and "mx:" before a name that has an MX record.
	dns    map[string][]string
	now    time.Time
	logged bytes.Buffer
	// admin and send are keys in full; adminKey is the admin key's record.
	admin, send       string
	adminKey, sendKey store.Key
}

func newRig(t *testing.T, adjust func(*console.Deps)) *rig {
	t.Helper()
	return newRigOn(t, func(_ *rig, d *console.Deps) {
		if adjust != nil {
			adjust(d)
		}
	})
}

// newRigOn is newRig for a console whose parts need the rig's own store.
func newRigOn(t *testing.T, adjust func(*rig, *console.Deps)) *rig {
	t.Helper()
	r := &rig{t: t, s: memstore.New(), now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	r.s.Now = func() time.Time { return r.now }
	r.ks = keys.New(r.s)
	var err error
	if r.admin, r.adminKey, err = r.ks.Create(context.Background(), "ops", store.ScopeAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if r.send, r.sendKey, err = r.ks.Create(context.Background(), "app", store.ScopeSend, ""); err != nil {
		t.Fatal(err)
	}
	r.mail = mail.New(r.s, capture.New(10, func() time.Time { return r.now }),
		mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute, KeepContent: 24 * time.Hour},
		slog.New(slog.NewTextHandler(&r.logged, nil)))
	d := console.Deps{Keys: r.ks, Mail: r.mail, Suppress: suppress.New(r.s),
		Webhooks: webhook.NewService(r.s, webhook.NewPoster(time.Second, true, 2, "test", time.Now), true, 20, 24*time.Hour, time.Now), Sessions: session.New(r.s, time.Hour), Driver: "fake", Version: "test",
		EventsOff: "Not read.", Log: slog.New(slog.NewTextHandler(&r.logged, nil))}
	d.Domains = domains.New(r.s, &dnscheck.Checker{Resolver: resolver{r}, Timeout: time.Second}, "cloudflare", cloudflare.Records)
	adjust(r, &d)
	r.h = console.New(d)
	return r
}

// call makes a request as a browser on the console's own site would, with
// a session cookie if one is given, and a form if one is given.
func (r *rig) call(method, path, session string, form url.Values) *httptest.ResponseRecorder {
	r.t.Helper()
	var body *strings.Reader = strings.NewReader("")
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, console.Root+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "cairnpost_console", Value: session})
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

// signIn signs in with a key and answers the session cookie's value.
func (r *rig) signIn(key string) string {
	r.t.Helper()
	rec := r.call("POST", "/signin", "", url.Values{"key": {key}})
	for _, c := range rec.Result().Cookies() {
		if c.Name == "cairnpost_console" && c.Value != "" {
			return c.Value
		}
	}
	r.t.Fatalf("no session: %d %s", rec.Code, rec.Body)
	return ""
}

func TestOnlyAnAdminKeySignsIn(t *testing.T) {
	r := newRig(t, nil)
	if rec := r.call("GET", "/", "", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/console/signin" {
		t.Fatalf("with no session: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	for key, want := range map[string]int{"": 401, "cp_nonsense": 401, r.send: 403, r.admin + "x": 401} {
		rec := r.call("POST", "/signin", "", url.Values{"key": {key}})
		if rec.Code != want || len(rec.Result().Cookies()) != 0 {
			t.Errorf("a key that is not an admin's: %d, want %d, cookies %v", rec.Code, want, rec.Result().Cookies())
		}
		// What was typed is never said back.
		if key != "" && strings.Contains(rec.Body.String(), key) {
			t.Errorf("the page repeats the key typed")
		}
	}
	rec := r.call("POST", "/signin", "", url.Values{"key": {" " + r.admin + "\n"}})
	c := cookieOf(rec, "cairnpost_console")
	if rec.Code != http.StatusSeeOther || c == nil {
		t.Fatalf("an admin key: %d %v", rec.Code, rec.Result().Cookies())
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/console" || c.MaxAge != 3600 || len(c.Value) < 40 {
		t.Fatalf("the session cookie: %+v", c)
	}
	page := r.call("GET", "/", c.Value, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "ops") {
		t.Fatalf("the overview: %d", page.Code)
	}
	// Who signed in is in the log, by the key's public prefix.
	if !strings.Contains(r.logged.String(), "signed in") || !strings.Contains(r.logged.String(), r.adminKey.Prefix) {
		t.Error("the sign-in was not logged")
	}
	// Neither the key nor the session's token is in a page or the log.
	for _, secret := range []string{r.admin, r.send, c.Value} {
		if strings.Contains(page.Body.String(), secret) || strings.Contains(r.logged.String(), secret) {
			t.Errorf("a secret is in a page or the log")
		}
	}
}

func TestTheCookieIsSecureUnlessToldOtherwise(t *testing.T) {
	r := newRig(t, func(d *console.Deps) { d.PlainHTTP = true })
	rec := r.call("POST", "/signin", "", url.Values{"key": {r.admin}})
	if c := cookieOf(rec, "cairnpost_console"); c == nil || c.Secure || !c.HttpOnly {
		t.Fatalf("%+v", c)
	}
}

// A session ends when its time is up, when it is signed out, and when its
// key is revoked.
func TestASessionEnds(t *testing.T) {
	r := newRig(t, nil)
	in := func(session string) bool { return r.call("GET", "/", session, nil).Code == 200 }
	old := r.signIn(r.admin)
	r.now = r.now.Add(59 * time.Minute)
	if !in(old) {
		t.Fatal("a session within its time was refused")
	}
	r.now = r.now.Add(time.Minute)
	if in(old) {
		t.Fatal("a session past its time was let in")
	}
	out := r.signIn(r.admin)
	if rec := r.call("POST", "/signout", out, url.Values{}); rec.Code != http.StatusSeeOther || in(out) {
		t.Fatalf("after signing out: %d, still in %v", rec.Code, in(out))
	}
	revoked := r.signIn(r.admin)
	if err := r.ks.Revoke(context.Background(), r.adminKey.Prefix); err != nil {
		t.Fatal(err)
	}
	if in(revoked) {
		t.Fatal("the session of a revoked key was let in")
	}
	if in("not-a-session") {
		t.Fatal("a made-up session was let in")
	}
}

// A change asked for from another site is refused before it is read, even
// with a live session.
func TestAChangeFromAnotherSiteIsRefused(t *testing.T) {
	r := newRig(t, nil)
	session := r.signIn(r.admin)
	for name, set := range map[string]func(*http.Request){
		"another site":   func(q *http.Request) { q.Header.Set("Sec-Fetch-Site", "cross-site") },
		"another origin": func(q *http.Request) { q.Header.Del("Sec-Fetch-Site"); q.Header.Set("Origin", "https://evil.example") },
	} {
		req := httptest.NewRequest("POST", "/console/keys", strings.NewReader("name=stolen&scope=admin"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(&http.Cookie{Name: "cairnpost_console", Value: session})
		set(req)
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if list, _ := r.ks.List(context.Background()); len(list) != 2 {
		t.Fatalf("a key was made from another site: %d keys", len(list))
	}
}

func TestEveryPageForbidsFramingCachingAndForeignScripts(t *testing.T) {
	r := newRig(t, nil)
	session := r.signIn(r.admin)
	for _, path := range []string{"/signin", "/", "/keys", "/nowhere"} {
		h := r.call("GET", path, session, nil).Header()
		if h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("%s: %v", path, h)
		}
	}
	// No page carries a script or a style of its own: the policy forbids both.
	for _, path := range []string{"/signin", "/", "/keys"} {
		body := r.call("GET", path, session, nil).Body.String()
		if strings.Contains(body, "<style") || strings.Contains(body, " style=") || strings.Contains(body, "onclick") ||
			strings.Count(body, "<script") != 1 || !strings.Contains(body, `<script src="/console/assets/console.js?v=`) {
			t.Errorf("%s carries a script or style of its own", path)
		}
	}
	if rec := r.call("GET", "/assets/console.css", "", nil); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("the stylesheet: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := r.call("GET", "/nowhere", session, nil); rec.Code != 404 {
		t.Fatalf("an unknown page: %d", rec.Code)
	}
}
