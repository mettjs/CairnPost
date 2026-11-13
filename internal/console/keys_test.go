package console_test

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/store"
)

var shown = regexp.MustCompile(`<code id="new-key">(cp_[0-9a-f]{8}_[A-Za-z0-9_-]{43})</code>`)

// A key is shown in full in the answer to the request that made it, and
// in no page after; what was typed is escaped wherever it is shown.
func TestAKeyIsMadeShownOnceAndRevoked(t *testing.T) {
	r := newRig(t, nil)
	session := r.signIn(r.admin)
	rec := r.call("POST", "/keys", session, url.Values{"name": {`billing <script>alert(1)</script>`}, "scope": {"send"}, "domain": {"Mail.Example.com"}})
	found := shown.FindStringSubmatch(rec.Body.String())
	if rec.Code != 201 || found == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	full := found[1]
	if strings.Contains(rec.Body.String(), "<script>alert") || !strings.Contains(rec.Body.String(), "billing &lt;script&gt;") {
		t.Fatal("the name typed was not escaped")
	}
	made, err := r.ks.Authenticate(context.Background(), full)
	if err != nil || made.Scope != store.ScopeSend || made.Domain != "mail.example.com" {
		t.Fatalf("the key shown is not a key: %+v %v", made, err)
	}
	list := r.call("GET", "/keys", session, nil).Body.String()
	if strings.Contains(list, full) || !strings.Contains(list, made.Prefix) || strings.Contains(r.logged.String(), full) {
		t.Fatal("the key is shown again, or logged, or missing from the list")
	}
	// Revoking asks first, then ends the key.
	if ask := r.call("GET", "/keys/"+made.Prefix+"/revoke", session, nil); ask.Code != 200 || !strings.Contains(ask.Body.String(), "cannot be undone") {
		t.Fatalf("%d", ask.Code)
	}
	if done := r.call("POST", "/keys/"+made.Prefix+"/revoke", session, url.Values{}); done.Code != 303 || done.Header().Get("Location") != "/console/keys" {
		t.Fatalf("%d %s", done.Code, done.Header().Get("Location"))
	}
	if _, err := r.ks.Authenticate(context.Background(), full); err == nil {
		t.Fatal("the revoked key still works")
	}
	if again := r.call("POST", "/keys/"+made.Prefix+"/revoke", session, url.Values{}); again.Code != 404 {
		t.Fatalf("revoking it twice: %d", again.Code)
	}
	// Who made and revoked it is in the log, by public prefixes, and on
	// the page: a key that leaks can be traced to the keys it made.
	for _, line := range []string{"a key was made", "a key was revoked", "by=" + r.adminKey.Prefix, "key=" + made.Prefix} {
		if !strings.Contains(r.logged.String(), line) {
			t.Errorf("the log does not hold %q", line)
		}
	}
	if made.CreatedBy != r.adminKey.Prefix || !strings.Contains(list, "made by <code>"+r.adminKey.Prefix) {
		t.Errorf("the key does not say who made it: %q", made.CreatedBy)
	}
}

func TestAKeyThatCannotBeMadeSaysWhy(t *testing.T) {
	r := newRig(t, nil)
	session := r.signIn(r.admin)
	for why, form := range map[string]url.Values{
		"no name":         {"name": {" "}, "scope": {"send"}},
		"a long name":     {"name": {strings.Repeat("n", 101)}, "scope": {"send"}},
		"a line break":    {"name": {"x\tadmin\nforged"}, "scope": {"send"}},
		"an escape":       {"name": {"x\x1b[2Jrow"}, "scope": {"send"}},
		"a NUL":           {"name": {"x\x00y"}, "scope": {"send"}},
		"not text":        {"name": {"x\xffy"}, "scope": {"send"}},
		"a port":          {"name": {"x"}, "scope": {"send"}, "domain": {"example.com:25"}},
		"a trailing dot":  {"name": {"x"}, "scope": {"send"}, "domain": {"example.com."}},
		"one label":       {"name": {"x"}, "scope": {"send"}, "domain": {"localhost"}},
		"no such scope":   {"name": {"x"}, "scope": {"root"}},
		"an address":      {"name": {"x"}, "scope": {"send"}, "domain": {"ana@example.com"}},
		"a domain a path": {"name": {"x"}, "scope": {"send"}, "domain": {"example.com/x"}},
	} {
		rec := r.call("POST", "/keys", session, form)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), `role="alert"`) || shown.MatchString(rec.Body.String()) {
			t.Errorf("%s: %d", why, rec.Code)
		}
	}
	if list, _ := r.ks.List(context.Background()); len(list) != 2 {
		t.Fatalf("%d keys", len(list))
	}
	// A hundred characters are a hundred characters in any script.
	if rec := r.call("POST", "/keys", session, url.Values{"name": {strings.Repeat("ж", 100)}, "scope": {"send"}}); rec.Code != 201 {
		t.Fatalf("a name of 100 characters, 200 bytes: %d", rec.Code)
	}
}

// A sending domain may have only so many live keys; a revoked one makes
// room.
func TestKeysAreLimitedPerSendingDomain(t *testing.T) {
	r := newRig(t, func(d *console.Deps) { d.Keys = d.Keys.WithMax(3) })
	session := r.signIn(r.admin)
	make := func() int {
		return r.call("POST", "/keys", session, url.Values{"name": {"one more"}, "scope": {"send"}}).Code
	}
	// Two keys bound to no domain exist: the rig's own.
	if make() != 201 {
		t.Fatal("there was room for one more")
	}
	full := r.call("POST", "/keys", session, url.Values{"name": {"one more"}, "scope": {"send"}})
	if full.Code != 409 || !strings.Contains(full.Body.String(), "as many keys that send from any domain as this server allows (CAIRNPOST_MAX_KEYS)") {
		t.Fatalf("the limit was not kept, or not said: %d", full.Code)
	}
	if rec := r.call("POST", "/keys", session, url.Values{"name": {"elsewhere"}, "scope": {"send"}, "domain": {"a.example.com"}}); rec.Code != 201 {
		t.Fatalf("another domain has room of its own: %d", rec.Code)
	}
	list, _ := r.ks.List(context.Background())
	for _, k := range list {
		if k.Name == "one more" {
			r.ks.Revoke(context.Background(), k.Prefix)
		}
	}
	if make() != 201 {
		t.Fatal("a revoked key did not make room")
	}
}

// An admin key bound to a sending domain sees, makes and revokes that
// domain's keys only: it cannot make itself a key with a wider reach.
func TestABoundAdminKeyIsConfinedToItsDomain(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	bound, _, err := r.ks.Create(ctx, "team-a ops", store.ScopeAdmin, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, other, _ := r.ks.Create(ctx, "team-b app", store.ScopeSend, "b.example.com")
	session := r.signIn(bound)
	list := r.call("GET", "/keys", session, nil).Body.String()
	if strings.Contains(list, other.Prefix) || strings.Contains(list, r.adminKey.Prefix) || !strings.Contains(list, "team-a ops") {
		t.Fatal("a bound key sees keys that are not its domain's")
	}
	for why, domain := range map[string]string{"another domain": "b.example.com", "its own": "a.example.com", "none named": ""} {
		rec := r.call("POST", "/keys", session, url.Values{"name": {"made by a"}, "scope": {"admin"}, "domain": {domain}})
		found := shown.FindStringSubmatch(rec.Body.String())
		if domain == "b.example.com" {
			if rec.Code != 403 || found != nil {
				t.Errorf("%s: %d", why, rec.Code)
			}
			continue
		}
		if found == nil {
			t.Fatalf("%s: %d", why, rec.Code)
		}
		if k, err := r.ks.Authenticate(ctx, found[1]); err != nil || k.Domain != "a.example.com" {
			t.Errorf("%s: the key made is bound to %q", why, k.Domain)
		}
	}
	for _, prefix := range []string{other.Prefix, r.adminKey.Prefix} {
		if ask := r.call("GET", "/keys/"+prefix+"/revoke", session, nil); ask.Code != 404 {
			t.Errorf("asked to revoke another domain's key: %d", ask.Code)
		}
		if done := r.call("POST", "/keys/"+prefix+"/revoke", session, url.Values{}); done.Code != 404 {
			t.Errorf("revoked another domain's key: %d", done.Code)
		}
	}
	if _, err := r.ks.Authenticate(ctx, r.admin); err != nil {
		t.Fatal("the unbound admin key was revoked by a bound one")
	}
}
