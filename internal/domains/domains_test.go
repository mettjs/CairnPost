package domains_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/driver/cloudflare"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// zone is a resolver over text records a test writes down.
type zone map[string][]string

func (z zone) LookupTXT(_ context.Context, name string) ([]string, error) {
	if got, ok := z[strings.TrimSuffix(name, ".")]; ok {
		return got, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (z zone) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if _, ok := z["mx:"+strings.TrimSuffix(name, ".")]; ok {
		return []*net.MX{{Host: "route1.mx.cloudflare.net."}}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func service(s store.Domains, z zone, records func(string) []driver.Record) *domains.Service {
	return domains.New(s, &dnscheck.Checker{Resolver: z, Timeout: time.Second}, "cloudflare", records)
}

// The domains known are those a live key is bound to and those mail was
// sent from; a key bound to one manages that one, known yet or not.
func TestTheSendingDomainsAreThoseOfKeysAndOfMail(t *testing.T) {
	ctx, db := context.Background(), memstore.New()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	db.Now = func() time.Time { return now }
	ks := keys.New(db)
	_, k, _ := ks.Create(ctx, "a", store.ScopeSend, "b.example.com")
	ks.Create(ctx, "b", store.ScopeAdmin, "b.example.com")
	_, gone, _ := ks.Create(ctx, "c", store.ScopeSend, "gone.example.com")
	ks.Revoke(ctx, gone.Prefix)
	ks.Create(ctx, "d", store.ScopeSend, "")
	for _, sender := range []string{"app@A.example.com", "app@b.example.com"} {
		if _, err := db.Start(ctx, store.Start{KeyID: k.ID, Sender: sender, Recipient: "ana@example.org", Driver: "test",
			TTL: time.Hour, InDoubt: time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	svc := service(db, zone{}, nil)
	list, more, err := svc.List(ctx, "")
	if err != nil || more || len(list) != 2 || list[0].Name != "a.example.com" || list[0].Keys != 0 || list[0].LastSent == nil ||
		list[1].Name != "b.example.com" || list[1].Keys != 2 || !list[1].LastSent.Equal(now) {
		t.Fatalf("%+v %v", list, err)
	}
	if list, _, err = svc.List(ctx, "b.example.com"); err != nil || len(list) != 1 || list[0].Keys != 2 {
		t.Fatalf("a bound key's: %+v %v", list, err)
	}
	if list, _, err = svc.List(ctx, "new.example.com"); err != nil || len(list) != 1 || list[0].Name != "new.example.com" || list[0].LastSent != nil {
		t.Fatalf("a bound key's domain nothing was sent from: %+v %v", list, err)
	}
}

// A check asks the provider's records and DMARC of a domain, and is
// ready when all are in place; a domain may have no DMARC record.
func TestADomainIsCheckedForWhatItsProviderAsks(t *testing.T) {
	ctx := context.Background()
	z := zone{
		"cf-bounce._domainkey.mail.example.com": {"v=DKIM1; p=abc"},
		"cf-bounce.mail.example.com":            {"v=spf1 include:_spf.mx.cloudflare.net ~all"},
		"mx:cf-bounce.mail.example.com":         {},
	}
	svc := service(nil, z, cloudflare.Records)
	r, err := svc.Check(ctx, "", " Mail.Example.com ")
	if err != nil || r.Domain != "mail.example.com" || !r.Asks || len(r.Results) != 4 || !r.Ready() {
		t.Fatalf("%+v %v", r, err)
	}
	delete(z, "cf-bounce._domainkey.mail.example.com")
	if r, _ = svc.Check(ctx, "", "mail.example.com"); r.Ready() || r.Results[0].State != dnscheck.Missing {
		t.Fatalf("with no key: %+v", r.Results[0])
	}
	if r.Wanting() != 1 || !r.NoPolicy() || len(r.Rows()) != 4 || r.Rows()[0].Words.Label != "Missing" {
		t.Fatalf("what is wanting: %d, %+v", r.Wanting(), r.Rows()[0])
	}
	// A provider that asks nothing is checked for DMARC alone.
	if r, err = service(nil, z, nil).Check(ctx, "", "mail.example.com"); err != nil || r.Asks || len(r.Results) != 1 || !r.Ready() {
		t.Fatalf("a provider that asks nothing: %+v %v", r, err)
	}
	// A key bound to a domain checks that one.
	if _, err = svc.Check(ctx, "mail.example.com", "MAIL.example.com"); err != nil {
		t.Fatalf("its own: %v", err)
	}
	for _, other := range []string{"example.com", "xmail.example.com", "a.mail.example.com"} {
		if _, err = svc.Check(ctx, "mail.example.com", other); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s, for a key bound to another: %v", other, err)
		}
	}
	if _, err = svc.Check(ctx, "", "not a name"); !errors.Is(err, domains.ErrNotAHost) {
		t.Fatalf("what is no host name: %v", err)
	}
}

// Every kind of record in every state has words of its own, which say
// nothing of a page: they are read on the command line too.
func TestEveryRecordInEveryStateHasWords(t *testing.T) {
	for _, use := range []string{driver.UseDKIM, driver.UseSPF, driver.UseBounce, dnscheck.UseDMARC} {
		for _, state := range dnscheck.States {
			res := dnscheck.Result{State: state, Policy: "reject"}
			res.Use, res.Include = use, "_spf.provider.example"
			w := domains.WordsFor(res)
			if w.Title == "" || w.Title == use || w.For == "" || w.Label == "" || w.Says == "" || strings.Contains(w.Says, "page") ||
				(w.Tone != "ok" && w.Tone != "failing" && w.Tone != "off") || (w.Tone == "ok") != (state == dnscheck.OK) {
				t.Errorf("%s, %s: %+v", use, state, w)
			}
		}
	}
	for _, policy := range []string{"none", "quarantine", "reject"} {
		res := dnscheck.Result{State: dnscheck.OK, Policy: policy, Inherited: "example.com"}
		res.Use = dnscheck.UseDMARC
		if says := domains.WordsFor(res).Says; !strings.Contains(says, "example.com may speak for it") || !strings.Contains(says, "Receivers are asked") {
			t.Errorf("%s: %q", policy, says)
		}
	}
	later := dnscheck.Result{State: "later"}
	later.Use = "bimi"
	if w := domains.WordsFor(later); w.Title != "bimi" || w.Tone != "off" || w.Label != "Not known" {
		t.Errorf("a record and a state not known here: %+v", w)
	}
	// A missing DMARC record is none, not a fault.
	none := dnscheck.Result{State: dnscheck.Missing}
	none.Use = dnscheck.UseDMARC
	if w := domains.WordsFor(none); w.Tone != "off" || w.Label != "None" {
		t.Errorf("no DMARC record: %+v", w)
	}
}

// A bounce address that points somewhere else is said to, apart from one
// that takes no mail at all.
func TestABounceAddressElsewhereSaysWhere(t *testing.T) {
	at := driver.Record{Use: driver.UseBounce, Name: "cf-bounce.mail.example.com", Type: "MX", Under: "mx.cloudflare.net"}
	elsewhere := domains.WordsFor(dnscheck.Result{Record: at, State: dnscheck.Wrong, Found: []string{"mx.other.example."}})
	null := domains.WordsFor(dnscheck.Result{Record: at, State: dnscheck.Wrong, Found: []string{"."}})
	if elsewhere.Tone != "failing" || !strings.Contains(elsewhere.Says, "does not point at the mail provider's servers (mx.cloudflare.net)") ||
		!strings.Contains(null.Says, "takes no mail") || elsewhere.Says == null.Says {
		t.Fatalf("elsewhere: %q\nnull: %q", elsewhere.Says, null.Says)
	}
}
