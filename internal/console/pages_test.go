package console_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/store"
)

// What a request did is said by the next page, once, and a URL cannot
// make a page say it.
func TestTheNextPageSaysWhatHappenedOnce(t *testing.T) {
	r := newRig(t, nil)
	b := &browser{r: r, secure: true, jar: map[string]string{}}
	b.follow("POST", "/signin", url.Values{"key": {r.admin}})
	_, other, _ := r.ks.Create(context.Background(), "old app", store.ScopeSend, "")
	page := b.follow("POST", "/keys/"+other.Prefix+"/revoke", url.Values{})
	if page.Code != 200 || !strings.Contains(page.Body.String(), "The key was revoked") {
		t.Fatalf("after a revoke: %d", page.Code)
	}
	if again := b.do("GET", "/keys", nil); strings.Contains(again.Body.String(), "The key was revoked") {
		t.Fatal("the page says it a second time")
	}
	for _, path := range []string{"/keys?notice=revoked", "/signin?notice=revoked", "/nowhere?notice=revoked"} {
		if rec := b.do("GET", path, nil); strings.Contains(rec.Body.String(), "was revoked") {
			t.Errorf("%s: a URL made the page claim something", path)
		}
	}
	// Revoking the key signed in with ends the session and says so.
	out := b.follow("POST", "/keys/"+r.adminKey.Prefix+"/revoke", url.Values{})
	if !strings.Contains(out.Body.String(), "You revoked the key you were signed in with") || !strings.Contains(out.Body.String(), `name="key"`) {
		t.Fatalf("after revoking one's own key: %d %s", out.Code, out.Body)
	}
}

// A session that ended says so; and a browser that did not keep the
// sign-in, as one on plain http does not, is told why.
func TestALostSessionIsExplained(t *testing.T) {
	r := newRig(t, nil)
	b := &browser{r: r, secure: true, jar: map[string]string{}}
	b.follow("POST", "/signin", url.Values{"key": {r.admin}})
	r.now = r.now.Add(2 * time.Hour)
	if page := b.follow("GET", "/keys", nil); !strings.Contains(page.Body.String(), "Your session ended") {
		t.Fatalf("an ended session: %s", page.Body)
	}
	plain := &browser{r: r, secure: false, jar: map[string]string{}}
	page := plain.follow("POST", "/signin", url.Values{"key": {r.admin}})
	if !strings.Contains(page.Body.String(), "did not keep the sign-in") || !strings.Contains(page.Body.String(), "CAIRNPOST_CONSOLE_PLAIN_HTTP") {
		t.Fatalf("a browser on plain http: %s", page.Body)
	}
	// Someone who was never signed in is told nothing.
	fresh := &browser{r: r, secure: true, jar: map[string]string{}}
	if page := fresh.follow("GET", "/", nil); strings.Contains(page.Body.String(), `role="status"`) {
		t.Fatal("a first visit is told something happened")
	}
}

// The stylesheet and the script are named by their content, so they may
// be kept for good and are never stale.
func TestAssetsAreNamedByTheirContent(t *testing.T) {
	r := newRig(t, nil)
	page := r.call("GET", "/signin", "", nil).Body.String()
	at := strings.Index(page, "console.css?v=")
	if at < 0 {
		t.Fatal("the stylesheet's URL has no version")
	}
	version := page[at+len("console.css?v=") : at+len("console.css?v=")+12]
	if rec := r.call("GET", "/assets/console.css?v="+version, "", nil); !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("named by its content: %q", rec.Header().Get("Cache-Control"))
	}
	if rec := r.call("GET", "/assets/console.css?v=other", "", nil); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("under another name: %q", rec.Header().Get("Cache-Control"))
	}
}

// A page that fails while it is made is answered as an error and logged
// as one, without what failed.
func TestAFailingPageIsAnErrorAndIsLogged(t *testing.T) {
	r := newRig(t, func(d *console.Deps) { d.Events = func() bool { panic("secret-in-a-panic") } })
	rec := r.call("GET", "/", r.signIn(r.admin), nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("%d", rec.Code)
	}
	logged := r.logged.String()
	if !strings.Contains(logged, "a handler panicked") || !strings.Contains(logged, "status=500") || strings.Contains(logged, "secret-in-a-panic") {
		t.Fatalf("the log: %s", logged)
	}
}
