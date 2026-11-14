package console_test

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/store"
)

// resolver answers from what a rig's DNS holds.
type resolver struct{ r *rig }

func (z resolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if got, ok := z.r.dns[strings.TrimSuffix(name, ".")]; ok {
		return got, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (z resolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if _, ok := z.r.dns["mx:"+strings.TrimSuffix(name, ".")]; ok {
		return []*net.MX{{Host: "route1.mx.cloudflare.net."}}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// The domains mail is sent from are listed, each leading to its check;
// any other can be checked by name before anything is sent from it.
func TestSendingDomainsAreListed(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	if page := b.do("GET", "/domains", nil).Body.String(); !strings.Contains(page, "No email has been sent yet") || !strings.Contains(page, `/domains?tab=check"`) ||
		!strings.Contains(b.do("GET", "/domains?tab=check", nil).Body.String(), `name="check"`) {
		t.Fatalf("an empty list: %s", page)
	}
	r.sent("app@Mail.Example.com", "ana@example.org", "one")
	r.sent("app@[192.0.2.1]", "ana@example.org", "two")
	r.ks.Create(context.Background(), "shop", store.ScopeSend, "shop.example.com")
	page := b.do("GET", "/domains", nil).Body.String()
	for _, want := range []string{`href="/console/domains/mail.example.com"`, `href="/console/domains/shop.example.com"`, "Nothing on record", "UTC",
		"[192.0.2.1]", "Not a name that can be looked up"} {
		if !strings.Contains(page, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	if strings.Contains(page, `/domains/[`) || strings.Contains(page, `/domains/%5B`) {
		t.Fatal("what is no host name is linked to a check")
	}
	if rec := b.do("GET", "/domains?check=+New.Example.com+", nil); rec.Code != 303 || rec.Header().Get("Location") != "/console/domains/new.example.com" {
		t.Fatalf("a domain typed: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	for _, typed := range []string{"a b", "ana@example.com", "http://example.com/", "localhost"} {
		rec := b.do("GET", "/domains?check="+strings.ReplaceAll(typed, " ", "+"), nil)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), "That is not a domain name") || rec.Header().Get("Location") != "" {
			t.Errorf("%q: %d", typed, rec.Code)
		}
	}
}

// A domain's records are looked up and each is said to be in place or
// not, with what to do; what DNS says is shown as text and never logged.
func TestADomainsDNSIsChecked(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	r.dns = map[string][]string{
		"cf-bounce._domainkey.mail.example.com": {"v=DKIM1; p=the-key-in-dns"},
		"cf-bounce.mail.example.com":            {"v=spf1 include:_spf.mx.cloudflare.net ~all"},
		"mx:cf-bounce.mail.example.com":         {},
		"_dmarc.example.com":                    {"v=DMARC1; p=reject; <b>odd</b>"},
	}
	rec := b.do("GET", "/domains/Mail.Example.com", nil)
	page := rec.Body.String()
	for _, want := range []string{">Ready<", "Every record the mail provider asks for is in place.",
		"<code>_dmarc.example.com</code>, as <code>_dmarc.mail.example.com</code> has none", "the-key-in-dns", "It names _spf.mx.cloudflare.net", "Bounces go to the mail provider&#39;s servers",
		"The one of example.com may speak for it", "Receivers are asked to refuse", "&lt;b&gt;odd&lt;/b&gt;",
		"<code>cf-bounce._domainkey.mail.example.com</code>", "this server cannot tell whether a key is the one"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if rec.Code != 200 || strings.Contains(page, "<b>odd") || strings.Contains(page, "need a look") {
		t.Fatalf("%d", rec.Code)
	}
	delete(r.dns, "cf-bounce._domainkey.mail.example.com")
	r.dns["cf-bounce.mail.example.com"] = []string{"v=spf1 include:other.example -all"}
	delete(r.dns, "_dmarc.example.com")
	page = b.do("GET", "/domains/mail.example.com", nil).Body.String()
	for _, want := range []string{"2 of 4 need a look", "No record was found where the mail provider puts its key", "does not name _spf.mx.cloudflare.net",
		">None<", "Mail still goes", "include:other.example"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// A domain nothing is known of is checked all the same; what is no
	// domain is not.
	if rec = b.do("GET", "/domains/new.example.com", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "3 of 4 need a look") {
		t.Fatalf("a new domain: %d", rec.Code)
	}
	for _, none := range []string{"localhost", "a@b.example", "[192.0.2.1]", "exa%20mple.com"} {
		if rec = b.do("GET", "/domains/"+none, nil); rec.Code != 404 {
			t.Errorf("%s: %d", none, rec.Code)
		}
	}
	if logged := r.logged.String(); strings.Contains(logged, "level=ERROR") || strings.Contains(logged, "the-key-in-dns") || strings.Contains(logged, "other.example") {
		t.Fatalf("the log holds what DNS said, or a failure that was none: %s", logged)
	}
}

// A provider that asks no records of a domain is said to; and a key
// bound to a domain sees and checks that one alone.
func TestADomainIsCheckedForItsProviderAndItsKey(t *testing.T) {
	r := newRigOn(t, func(r *rig, d *console.Deps) {
		d.Domains = domains.New(r.s, &dnscheck.Checker{Resolver: resolver{r}, Timeout: 1e9}, "capture", nil)
	})
	if page := r.signedIn(r.admin).do("GET", "/domains/mail.example.com", nil).Body.String(); !strings.Contains(page, "asks no DNS records of a domain") ||
		!strings.Contains(page, "<code>capture</code>") || !strings.Contains(page, ">Ready<") || strings.Contains(page, "is in place") ||
		!strings.Contains(page, "It has no DMARC policy, which is allowed.") {
		t.Fatalf("a provider that asks nothing: %s", page)
	}
	r = newRig(t, nil)
	r.sent("app@other.example.com", "ana@example.org", "one")
	bound, _, err := r.ks.Create(context.Background(), "a's admin", store.ScopeAdmin, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	b := r.signedIn(bound)
	page := b.do("GET", "/domains", nil).Body.String()
	if !strings.Contains(page, `href="/console/domains/a.example.com"`) || strings.Contains(page, "other.example.com") || strings.Contains(page, `name="check"`) {
		t.Fatalf("a bound key's list: %s", page)
	}
	if rec := b.do("GET", "/domains/a.example.com", nil); rec.Code != 200 {
		t.Fatalf("its own domain: %d", rec.Code)
	}
	for _, other := range []string{"other.example.com", "example.com", "xa.example.com"} {
		if rec := b.do("GET", "/domains/"+other, nil); rec.Code != 404 {
			t.Errorf("%s: %d", other, rec.Code)
		}
	}
	if rec := b.do("GET", "/domains?check=other.example.com", nil); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
}
