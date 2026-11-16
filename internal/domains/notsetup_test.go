package domains_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/driver/cloudflare"
)

// down is a resolver that answers nothing: every lookup fails.
type down struct{ asked atomic.Int32 }

func (d *down) LookupTXT(context.Context, string) ([]string, error) {
	d.asked.Add(1)
	return nil, errors.New("no answer")
}

func (d *down) LookupMX(context.Context, string) ([]*net.MX, error) {
	d.asked.Add(1)
	return nil, errors.New("no answer")
}

func ready() zone {
	return zone{
		"cf-bounce._domainkey.mail.example.com": {"v=DKIM1; p=abc"},
		"cf-bounce.mail.example.com":            {"v=spf1 include:_spf.mx.cloudflare.net ~all"},
		"mx:cf-bounce.mail.example.com":         {},
	}
}

// A domain is not set up when none of the records its provider asks is
// in place. One record awry on a domain that has the others is not that:
// such a domain sends, and an email it is refused is the email's fault.
func TestADomainIsNotSetUpWhenNoneOfItsRecordsIsInPlace(t *testing.T) {
	ctx := context.Background()
	rec, bad := service(nil, zone{}, cloudflare.Records).NotSetUp(ctx, "Mail.Example.com")
	if !bad || rec.Name != "cf-bounce._domainkey.mail.example.com" || rec.Type != "TXT" {
		t.Fatalf("nothing in DNS: %+v %v", rec, bad)
	}
	// Another's records at the same names: there, and not the provider's.
	others := zone{
		"cf-bounce._domainkey.mail.example.com": {"a wildcard's text"},
		"cf-bounce.mail.example.com":            {"v=spf1 -all"},
	}
	if _, bad = service(nil, others, cloudflare.Records).NotSetUp(ctx, "mail.example.com"); !bad {
		t.Fatal("records that are not the provider's")
	}
	if rec, bad = service(nil, ready(), cloudflare.Records).NotSetUp(ctx, "mail.example.com"); bad {
		t.Fatalf("every record in place, and no DMARC: %+v", rec)
	}
	for _, awry := range []string{"cf-bounce._domainkey.mail.example.com", "cf-bounce.mail.example.com", "mx:cf-bounce.mail.example.com"} {
		z := ready()
		delete(z, awry)
		if _, bad = service(nil, z, cloudflare.Records).NotSetUp(ctx, "mail.example.com"); bad {
			t.Errorf("only %s missing: called not set up", awry)
		}
	}
	if _, bad = service(nil, zone{}, nil).NotSetUp(ctx, "mail.example.com"); bad {
		t.Fatal("a provider that asks no record")
	}
	if _, bad = service(nil, zone{}, cloudflare.Records).NotSetUp(ctx, "not a name"); bad {
		t.Fatal("what is no host name")
	}
}

// A lookup that fails says nothing either way: the domain is left alone.
// That nothing was learned is kept a while too, so a resolver that does
// not answer is not waited for by every refused email.
func TestALookupThatFailsLeavesTheDomainAlone(t *testing.T) {
	d := &down{}
	svc := domains.New(nil, &dnscheck.Checker{Resolver: d, Timeout: time.Second}, "cloudflare", cloudflare.Records)
	for range 3 {
		if rec, bad := svc.NotSetUp(context.Background(), "mail.example.com"); bad {
			t.Fatalf("with DNS down: %+v", rec)
		}
	}
	if got := d.asked.Load(); got != 3 {
		t.Fatalf("%d lookups, want the 3 of one look", got)
	}
}

// A caller that goes before the look ends leaves nothing behind: the
// next one looks for itself.
func TestALookCutShortIsNotKept(t *testing.T) {
	z := zone{}
	svc := service(nil, z, cloudflare.Records)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	svc.NotSetUp(gone, "mail.example.com")
	for name, records := range ready() {
		z[name] = records
	}
	if _, bad := svc.NotSetUp(context.Background(), "mail.example.com"); bad {
		t.Fatal("the answer of a look that was cut short was kept")
	}
}
