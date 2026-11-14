package console_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// listener is an application's endpoint on this machine: it answers
// with a status the test sets and counts what it was sent.
type listener struct {
	*httptest.Server
	status atomic.Int64
	hits   atomic.Int64
}

func listen(t *testing.T) *listener {
	l := &listener{}
	l.status.Store(204)
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.hits.Add(1)
		w.WriteHeader(int(l.status.Load()))
	}))
	t.Cleanup(l.Close)
	return l
}

// token is in every endpoint URL the tests make: a URL can hold one, so
// it must never be logged, put in an address or carried in a cookie.
const token = "tok-in-the-url"

// leaks fails the test if an answer or the log holds the URL's token
// where it must not be.
func (r *rig) leaks(rec *httptest.ResponseRecorder, when string) {
	r.t.Helper()
	held := r.logged.String() + rec.Header().Get("Location")
	for _, c := range rec.Result().Cookies() {
		held += c.Value
	}
	if strings.Contains(held, token) || strings.Contains(held, "whsec_") || strings.Contains(r.logged.String(), "127.0.0.1") {
		r.t.Fatalf("%s: the endpoint's URL or secret is in the log, an address or a cookie", when)
	}
}

// made makes an endpoint through the console and answers its id.
func (r *rig) made(b *browser, form url.Values) string {
	r.t.Helper()
	if form.Get("hears") == "" {
		form.Set("hears", "all")
	}
	rec := b.do("POST", "/webhooks", form)
	page := rec.Body.String()
	at := strings.Index(page, `/console/webhooks/`)
	if rec.Code != 201 || at < 0 {
		r.t.Fatalf("an endpoint was not made: %d %s", rec.Code, page)
	}
	id := page[at+len(`/console/webhooks/`):]
	return id[:strings.IndexAny(id, `"/?`)]
}

// An endpoint is made with what it hears about chosen, and its secret
// is shown once.
func TestAnEndpointIsMadeAndItsSecretShownOnce(t *testing.T) {
	r := newRig(t, nil)
	b, l := r.signedIn(r.admin), listen(t)
	hook := l.URL + "/hook?token=" + token
	rec := b.do("POST", "/webhooks", url.Values{"url": {hook}, "hears": {"some"}, "events": {"email.bounced", "email.complained"},
		"description": {"the shop"}, "domain": {"Mail.Example.com"}})
	page := rec.Body.String()
	if rec.Code != 201 || !strings.Contains(page, "whsec_") || !strings.Contains(page, "shown this once") {
		t.Fatalf("%d %s", rec.Code, page)
	}
	r.leaks(rec, "after a create")
	list := b.do("GET", "/webhooks", nil).Body.String()
	for _, want := range []string{token, "the shop", "<code>mail.example.com</code>", "<code>email.bounced</code>", "<code>email.complained</code>", ">On<"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	if strings.Contains(list, "whsec_") || strings.Contains(list, "email.delivered</code>,") {
		t.Fatal("the secret is shown again, or an event that was not chosen")
	}
	if logged := r.logged.String(); !strings.Contains(logged, "a webhook endpoint was made") || !strings.Contains(logged, "by="+r.adminKey.Prefix) ||
		!strings.Contains(logged, "domain=mail.example.com") {
		t.Fatalf("the log: %s", logged)
	}
	for what, c := range map[string]struct {
		form url.Values
		want string
	}{
		"only these, none ticked":  {url.Values{"url": {hook}, "hears": {"some"}}, "Tick at least one event"},
		"not a URL":                {url.Values{"url": {"app.example.com"}}, "The URL must be an address starting with"},
		"a name in the URL":        {url.Values{"url": {"https://ana:pw@app.example.com/"}}, "The URL must not carry a user name"},
		"an event that is none":    {url.Values{"url": {hook}, "hears": {"some"}, "events": {"email.opened"}}, "Tick at least one event"},
		"every event, some ticked": {url.Values{"url": {hook}, "hears": {"all"}, "events": {"email.bounced"}}, "Events are ticked, and"},
		"neither chosen":           {url.Values{"url": {hook}, "hears": {"both"}}, "Choose &#34;Every event&#34; or"},
		"a space inside the URL":   {url.Values{"url": {"https://app.example.com/a b"}}, "The URL must not have a space in it"},
		"a description of lines":   {url.Values{"url": {hook}, "description": {"a\nb"}}, "The description must be one line"},
		"not a domain":             {url.Values{"url": {hook}, "domain": {"a@b"}}, "The sending domain must be a name"},
		"too much":                 {url.Values{"url": {hook}, "description": {strings.Repeat("d", 1<<20)}}, "more than this form takes"},
	} {
		if c.form.Get("hears") == "" {
			c.form.Set("hears", "all")
		}
		rec := b.do("POST", "/webhooks", c.form)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), c.want) || rec.Body.Len() > 40000 {
			t.Errorf("%s: %d", what, rec.Code)
		}
		r.leaks(rec, what)
	}
	// A URL pasted with space around it is kept without it.
	spaced := r.made(b, url.Values{"url": {"  " + hook + "-spaced \n"}})
	if e, _ := r.s.Endpoint(context.Background(), spaced); e.URL != hook+"-spaced" {
		t.Fatalf("kept as %q", e.URL)
	}
	// A refused form says so at the top, and keeps what was typed and
	// ticked, also under a choice that contradicts the ticks.
	page = b.do("POST", "/webhooks", url.Values{"url": {"nope"}, "hears": {"some"}, "events": {"email.bounced"}, "description": {"kept"}}).Body.String()
	if !strings.Contains(page, `value="nope"`) || !strings.Contains(page, `value="kept"`) || !strings.Contains(page, `value="email.bounced" checked`) ||
		!strings.Contains(page, `value="some" checked`) || !strings.Contains(page, `The endpoint was not made.`) {
		t.Fatal("a refused form came back empty")
	}
	page = b.do("POST", "/webhooks", url.Values{"url": {hook}, "hears": {"all"}, "events": {"email.failed"}}).Body.String()
	if !strings.Contains(page, `value="email.failed" checked`) || !strings.Contains(page, `value="all" checked`) {
		t.Fatal("the ticks of a refused form were dropped")
	}
}

// A sending domain has only so many endpoints, and the page says which
// setting that is.
func TestEndpointsAreLimited(t *testing.T) {
	r := newRigOn(t, func(r *rig, d *console.Deps) {
		d.Webhooks = webhook.NewService(r.s, webhook.NewPoster(time.Second, true, 2, "test", time.Now), true, 1, 24*time.Hour, time.Now)
	})
	b := r.signedIn(r.admin)
	r.made(b, url.Values{"url": {"https://app.example.com/a"}})
	rec := b.do("POST", "/webhooks", url.Values{"url": {"https://app.example.com/b"}, "hears": {"all"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "CAIRNPOST_WEBHOOK_MAX_ENDPOINTS") ||
		!strings.Contains(rec.Body.String(), "Every sending domain together has 1 endpoint already") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	r.made(b, url.Values{"url": {"https://app.example.com/c"}, "domain": {"a.example.com"}})
	rec = b.do("POST", "/webhooks", url.Values{"url": {"https://app.example.com/d"}, "hears": {"all"}, "domain": {"a.example.com"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "That sending domain has 1 endpoint already") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// An endpoint is changed, turned off and on, tested and removed; each
// is said by the page it leads to, once.
func TestAnEndpointIsChangedTestedAndRemoved(t *testing.T) {
	r := newRig(t, nil)
	b, l := r.signedIn(r.admin), listen(t)
	hook := l.URL + "/hook?token=" + token
	id := r.made(b, url.Values{"url": {hook}, "hears": {"some"}, "events": {"email.bounced"}})
	at := "/webhooks/" + id
	page := b.do("GET", at, nil).Body.String()
	if !strings.Contains(page, token) || !strings.Contains(page, `value="email.bounced" checked`) || !strings.Contains(page, "Nothing has been sent to it yet") {
		t.Fatalf("the endpoint's page: %s", page)
	}
	// To every event, and it stays every event through another change.
	for _, description := range []string{"first", "second"} {
		rec := b.do("POST", at, url.Values{"url": {hook}, "hears": {"all"}, "description": {description}})
		if rec.Code != 303 || rec.Header().Get("Location") != "/console"+at {
			t.Fatalf("a change: %d %s", rec.Code, rec.Header().Get("Location"))
		}
		r.leaks(rec, "after a change")
		if page = b.do("GET", at, nil).Body.String(); !strings.Contains(page, "The endpoint was changed.") || !strings.Contains(page, `value="all" checked`) ||
			strings.Contains(page, `" checked> <code>`) || !strings.Contains(page, `value="`+description+`"`) {
			t.Fatalf("after changing to every event (%s): %s", description, page)
		}
	}
	if e, _ := r.s.Endpoint(context.Background(), id); len(e.Kinds) != 0 || e.Description != "second" {
		t.Fatalf("what is kept: %+v", e)
	}
	// A refused change keeps what was typed and changes nothing.
	rec := b.do("POST", at, url.Values{"url": {"ftp://" + token + ".example.com"}, "hears": {"some"}, "events": {"email.failed"}})
	if page = rec.Body.String(); rec.Code != 422 || !strings.Contains(page, "The URL must be an address") || !strings.Contains(page, `value="ftp://`) ||
		!strings.Contains(page, `value="email.failed" checked`) {
		t.Fatalf("a refused change: %d", rec.Code)
	}
	r.leaks(rec, "after a refused change")
	// Off is off however often it is said.
	for range 2 {
		if page = b.follow("POST", at+"/enabled", url.Values{"enabled": {"off"}}).Body.String(); !strings.Contains(page, "The endpoint is off.") ||
			!strings.Contains(page, "it is told nothing of what happens meanwhile") {
			t.Fatalf("after turning it off: %s", page)
		}
	}
	// A form that says neither changes nothing.
	for _, neither := range []url.Values{{}, {"enabled": {"true"}}} {
		if rec = b.do("POST", at+"/enabled", neither); rec.Code != 422 {
			t.Fatalf("on or off, unsaid: %d", rec.Code)
		}
	}
	if page = b.follow("POST", at+"/enabled", url.Values{"enabled": {"on"}}).Body.String(); !strings.Contains(page, "The endpoint is on.") ||
		!strings.Contains(page, "what happened while it was off is not") {
		t.Fatal("after turning it on")
	}
	// A test is sent once, and what it met is said once.
	rec = b.do("POST", at+"/test", url.Values{})
	if rec.Code != 303 || l.hits.Load() != 1 {
		t.Fatalf("a test: %d, %d sent", rec.Code, l.hits.Load())
	}
	r.leaks(rec, "after a test")
	if page = b.do("GET", at, nil).Body.String(); !strings.Contains(page, "The endpoint took the test webhook. It answered 204.") {
		t.Fatalf("after a test: %s", page)
	}
	if page = b.do("GET", at, nil).Body.String(); strings.Contains(page, "test webhook") {
		t.Fatal("it is said twice")
	}
	l.status.Store(500)
	if page = b.follow("POST", at+"/test", url.Values{}).Body.String(); !strings.Contains(page, "The test webhook was not taken. It answered 500, which is not a success.") {
		t.Fatalf("a test that failed: %s", page)
	}
	// A form's own address, asked for, sends nothing.
	for _, formOnly := range []string{"/test", "/enabled", "/deliveries/x/retry"} {
		if rec = b.do("GET", at+formOnly, nil); rec.Code != 303 || rec.Header().Get("Location") != "/console"+at {
			t.Errorf("GET %s: %d", formOnly, rec.Code)
		}
	}
	if l.hits.Load() != 2 {
		t.Fatalf("%d webhooks were sent, want the two tests", l.hits.Load())
	}
	// Removing asks first, and says what goes with it.
	if page = b.do("GET", at+"/delete", nil).Body.String(); !strings.Contains(page, "still owed are dropped") || !strings.Contains(page, "turn it off instead") ||
		!strings.Contains(page, "told nothing of what happens while it is off") {
		t.Fatalf("the question: %s", page)
	}
	rec = b.do("POST", at+"/delete", url.Values{})
	if rec.Code != 303 || rec.Header().Get("Location") != "/console/webhooks" {
		t.Fatalf("removing: %d", rec.Code)
	}
	r.leaks(rec, "after removing")
	if page = b.do("GET", "/webhooks", nil).Body.String(); !strings.Contains(page, "The endpoint was removed") || !strings.Contains(page, "There are no endpoints yet") {
		t.Fatalf("after removing: %s", page)
	}
	if rec = b.do("GET", at, nil); rec.Code != 404 {
		t.Fatalf("a removed endpoint: %d", rec.Code)
	}
	logged := r.logged.String()
	for _, want := range []string{"was changed", "was turned on or off", "enabled=false", "was tested", "status=204", "failure=status", "was removed", "endpoint=" + id} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log lacks %q", want)
		}
	}
}

// An endpoint on a private address is not sent to unless the server
// allows it, and the page says which setting does.
func TestABlockedEndpointSaysWhy(t *testing.T) {
	r := newRigOn(t, func(r *rig, d *console.Deps) {
		d.Webhooks = webhook.NewService(r.s, webhook.NewPoster(time.Second, false, 2, "test", time.Now), false, 20, 24*time.Hour, time.Now)
	})
	b := r.signedIn(r.admin)
	id := r.made(b, url.Values{"url": {"https://127.0.0.1:9/hook"}})
	if page := b.follow("POST", "/webhooks/"+id+"/test", url.Values{}).Body.String(); !strings.Contains(page, "CAIRNPOST_WEBHOOK_ALLOW_PRIVATE") {
		t.Fatalf("a blocked test: %s", page)
	}
}

// What an endpoint was sent is listed, narrowed and tried again; ids
// made by hand are answered plainly, never as a failure.
func TestAnEndpointsDeliveriesAreShownAndRetried(t *testing.T) {
	r := newRig(t, nil)
	b, l := r.signedIn(r.admin), listen(t)
	id := r.made(b, url.Values{"url": {l.URL + "/a?token=" + token}})
	other := r.made(b, url.Values{"url": {l.URL + "/b?token=" + token}})
	m := r.sent("app@example.com", "ana@example.org", "one")
	r.heard(m, store.EventBounced, "5.1.1", "")
	at := "/webhooks/" + id
	page := b.do("GET", at, nil).Body.String()
	for _, want := range []string{"<code>email.bounced</code>", ">Waiting<", "/emails/" + m.ID, "Try now", "Next try"} {
		if !strings.Contains(page, want) {
			t.Errorf("the deliveries lack %q: %s", want, page)
		}
	}
	mine, _ := r.s.ListDeliveries(context.Background(), id, "", "", 10)
	theirs, _ := r.s.ListDeliveries(context.Background(), other, "", "", 10)
	if len(mine) != 1 || len(theirs) != 1 {
		t.Fatalf("%d and %d deliveries", len(mine), len(theirs))
	}
	if page = b.follow("POST", at+"/deliveries/"+mine[0].ID+"/retry", url.Values{"in": {"pending"}}).Body.String(); !strings.Contains(page, "is being tried again now") ||
		!strings.Contains(page, `value="pending" selected`) {
		t.Fatalf("after a retry: %s", page)
	}
	// What is done from a page of the list leads back to that page.
	rec := b.do("POST", at+"/deliveries/"+mine[0].ID+"/retry", url.Values{"in": {"pending"}, "at": {"dlv-9999"}})
	if rec.Header().Get("Location") != "/console"+at+"?after=dlv-9999&state=pending" {
		t.Fatalf("a retry from an older page leads to %s", rec.Header().Get("Location"))
	}
	if rec = b.do("POST", at+"/test", url.Values{"at": {"a b\"<"}, "in": {"nonsense"}}); rec.Header().Get("Location") != "/console"+at {
		t.Fatalf("a cursor that is none reached an address: %s", rec.Header().Get("Location"))
	}
	// Off, nothing is tried now, and the page does not say it is.
	b.do("POST", at+"/enabled", url.Values{"enabled": {"off"}})
	b.do("GET", at, nil)
	if page = b.follow("POST", at+"/deliveries/"+mine[0].ID+"/retry", url.Values{}).Body.String(); !strings.Contains(page, "tried when it is turned on") ||
		strings.Contains(page, "Next try") || !strings.Contains(page, "Waits until the endpoint is on") || !strings.Contains(page, "Try when on") {
		t.Fatalf("a retry of an endpoint that is off: %s", page)
	}
	b.do("POST", at+"/enabled", url.Values{"enabled": {"on"}})
	b.do("GET", at, nil)
	for _, none := range []string{theirs[0].ID, "nonsense", "00000000-0000-7000-8000-000000000000"} {
		if page = b.follow("POST", at+"/deliveries/"+none+"/retry", url.Values{}).Body.String(); !strings.Contains(page, "delivered meanwhile, or is no longer on record") {
			t.Errorf("a retry of %s: %s", none, page)
		}
	}
	if page = b.do("GET", at+"?state=delivered", nil).Body.String(); !strings.Contains(page, "Nothing it was sent is in that state") {
		t.Fatal("a state with nothing in it is not said")
	}
	if page = b.do("GET", at+"?state=nonsense", nil).Body.String(); !strings.Contains(page, ">Waiting<") {
		t.Fatal("a state that is none narrowed the list")
	}
	for _, after := range []string{"nonsense", strings.Repeat("a", 100), "a\x00b"} {
		if rec := b.do("GET", at+"?after="+url.QueryEscape(after), nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "There are no older ones") {
			t.Errorf("after %q: %d", after, rec.Code)
		}
	}
	for _, none := range []string{"nonsense", "00000000-0000-7000-8000-000000000000"} {
		if rec := b.do("GET", "/webhooks/"+none, nil); rec.Code != 404 {
			t.Errorf("endpoint %s: %d", none, rec.Code)
		}
	}
	if strings.Contains(r.logged.String(), "level=ERROR") || strings.Contains(r.logged.String(), token) {
		t.Fatalf("a hand-made id was logged as a failure, or a URL was logged: %s", r.logged.String())
	}
}

// A key that manages one sending domain sees and changes that domain's
// endpoints: another's is, on every page and to every form, not there.
func TestABoundKeyManagesItsDomainsEndpoints(t *testing.T) {
	r := newRig(t, nil)
	all, l := r.signedIn(r.admin), listen(t)
	ours := r.made(all, url.Values{"url": {l.URL + "/ours"}, "domain": {"a.example.com"}})
	theirs := r.made(all, url.Values{"url": {l.URL + "/theirs"}, "domain": {"b.example.com"}})
	every := r.made(all, url.Values{"url": {l.URL + "/every"}})
	bound, _, err := r.ks.Create(context.Background(), "a's admin", store.ScopeAdmin, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	b := r.signedIn(bound)
	page := b.do("GET", "/webhooks", nil).Body.String()
	if !strings.Contains(page, "/ours") || strings.Contains(page, "/theirs") || strings.Contains(page, "/every") {
		t.Fatalf("the list: %s", page)
	}
	if rec := b.do("GET", "/webhooks/"+ours, nil); rec.Code != 200 {
		t.Fatalf("its own endpoint: %d", rec.Code)
	}
	for _, id := range []string{theirs, every} {
		for _, c := range [][2]string{{"GET", ""}, {"POST", ""}, {"POST", "/enabled"}, {"POST", "/test"}, {"POST", "/deliveries/x/retry"},
			{"GET", "/delete"}, {"POST", "/delete"}} {
			form := url.Values{"url": {"https://evil.example.com/"}, "enabled": {"off"}}
			if c[0] == "GET" {
				form = nil
			}
			if rec := b.do(c[0], "/webhooks/"+id+c[1], form); rec.Code != 404 {
				t.Errorf("%s %s of another's endpoint: %d", c[0], c[1], rec.Code)
			}
		}
		if e, err := r.s.Endpoint(context.Background(), id); err != nil || !e.Enabled || strings.Contains(e.URL, "evil") {
			t.Fatalf("another's endpoint was changed: %+v %v", e, err)
		}
	}
	if l.hits.Load() != 0 {
		t.Fatal("another's endpoint was sent a test")
	}
	if rec := b.do("POST", "/webhooks", url.Values{"url": {l.URL + "/x"}, "hears": {"all"}, "domain": {"b.example.com"}}); rec.Code != 403 {
		t.Fatalf("making one for another domain: %d", rec.Code)
	}
	// Its own domain needs no naming.
	made := r.made(b, url.Values{"url": {l.URL + "/new"}})
	if e, _ := r.s.Endpoint(context.Background(), made); e.Domain != "a.example.com" {
		t.Fatalf("made for %q", e.Domain)
	}
}
