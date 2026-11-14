package dnscheck_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/driver"
)

// zone is a resolver over records a test writes down. A name in down
// fails as a server that does not answer; any other name not written
// down has no such record.
type zone struct {
	txt  map[string][]string
	mx   map[string][]string
	down map[string]bool
	slow time.Duration
	// asked counts the lookups, if it is set.
	asked *atomic.Int64
}

// whole answers a name as the zone writes it down, and fails for one
// that was not asked whole: a name with no dot at its end is tried by a
// resolver under the search domains of the machine it runs on.
func whole(name string) (string, error) {
	bare, rooted := strings.CutSuffix(name, ".")
	if !rooted {
		return "", errors.New("asked for a name that is not rooted: " + name)
	}
	return bare, nil
}

func (z zone) fail(ctx context.Context, name string) error {
	if z.asked != nil {
		z.asked.Add(1)
	}
	if z.slow > 0 {
		select {
		case <-ctx.Done():
			return &net.DNSError{Err: "i/o timeout", Name: name, IsTimeout: true}
		case <-time.After(z.slow):
		}
	}
	if z.down[name] {
		return &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	return nil
}

func (z zone) LookupTXT(ctx context.Context, name string) ([]string, error) {
	name, err := whole(name)
	if err != nil {
		return nil, err
	}
	if err := z.fail(ctx, name); err != nil {
		return nil, err
	}
	if got, ok := z.txt[name]; ok {
		return got, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (z zone) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	name, err := whole(name)
	if err != nil {
		return nil, err
	}
	if err := z.fail(ctx, name); err != nil {
		return nil, err
	}
	var out []*net.MX
	for _, host := range z.mx[name] {
		out = append(out, &net.MX{Host: host})
	}
	if out == nil {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return out, nil
}

var asked = []driver.Record{
	{Use: driver.UseDKIM, Name: "sel._domainkey.mail.example.com", Type: "TXT"},
	{Use: driver.UseSPF, Name: "bounce.mail.example.com", Type: "TXT", Include: "_spf.provider.example"},
	{Use: driver.UseBounce, Name: "bounce.mail.example.com", Type: "MX", Under: "provider.example"},
}

// check answers the state of each record by its use.
func check(t *testing.T, z zone, domain string) map[string]dnscheck.Result {
	t.Helper()
	c := &dnscheck.Checker{Resolver: z, Timeout: 200 * time.Millisecond}
	results, err := c.Check(context.Background(), domain, asked)
	if err != nil || len(results) != 4 {
		t.Fatalf("%d results: %v", len(results), err)
	}
	out := map[string]dnscheck.Result{}
	for _, r := range results {
		out[r.Use] = r
	}
	return out
}

func TestEachRecordIsInPlaceMissingOrDifferent(t *testing.T) {
	good := zone{
		txt: map[string][]string{
			"sel._domainkey.mail.example.com": {"v=DKIM1; k=rsa; p=MIIBIjANBgkq"},
			"bounce.mail.example.com":         {"some-verification=abc", "v=spf1 include:_SPF.provider.example ~all"},
			"_dmarc.mail.example.com":         {"v=DMARC1; p=Reject;"},
		},
		mx: map[string][]string{"bounce.mail.example.com": {"mx1.provider.example."}},
	}
	got := check(t, good, " Mail.Example.com ")
	for use, r := range got {
		if r.State != dnscheck.OK || len(r.Found) != 1 {
			t.Errorf("%s: %s %v", use, r.State, r.Found)
		}
	}
	if d := got[dnscheck.UseDMARC]; d.Policy != "reject" || d.Inherited != "" || d.Name != "_dmarc.mail.example.com" {
		t.Errorf("dmarc: %+v", d)
	}
	for what, c := range map[string]struct {
		change func(z *zone)
		use    string
		want   string
	}{
		"no key":        {func(z *zone) { delete(z.txt, "sel._domainkey.mail.example.com") }, driver.UseDKIM, dnscheck.Missing},
		"a key revoked": {func(z *zone) { z.txt["sel._domainkey.mail.example.com"] = []string{"v=DKIM1; p="} }, driver.UseDKIM, dnscheck.Wrong},
		"not a key":     {func(z *zone) { z.txt["sel._domainkey.mail.example.com"] = []string{"hello"} }, driver.UseDKIM, dnscheck.Wrong},
		"no spf":        {func(z *zone) { z.txt["bounce.mail.example.com"] = []string{"some-verification=abc"} }, driver.UseSPF, dnscheck.Missing},
		"another's spf": {func(z *zone) { z.txt["bounce.mail.example.com"] = []string{"v=spf1 include:other.example -all"} }, driver.UseSPF, dnscheck.Wrong},
		"an include that only begins alike": {func(z *zone) {
			z.txt["bounce.mail.example.com"] = []string{"v=spf1 include:_spf.provider.example.evil ~all"}
		}, driver.UseSPF, dnscheck.Wrong},
		"nowhere for bounces":           {func(z *zone) { delete(z.mx, "bounce.mail.example.com") }, driver.UseBounce, dnscheck.Missing},
		"a name that takes no mail":     {func(z *zone) { z.mx["bounce.mail.example.com"] = []string{"."} }, driver.UseBounce, dnscheck.Wrong},
		"bounces sent to somebody else": {func(z *zone) { z.mx["bounce.mail.example.com"] = []string{"mx.other.example."} }, driver.UseBounce, dnscheck.Wrong},
		"one host of several is somebody else's": {func(z *zone) {
			z.mx["bounce.mail.example.com"] = []string{"mx1.provider.example.", "mx.other.example."}
		}, driver.UseBounce, dnscheck.Wrong},
		"a host that only ends alike": {func(z *zone) { z.mx["bounce.mail.example.com"] = []string{"mx.evilprovider.example."} }, driver.UseBounce, dnscheck.Wrong},
		"the provider's hosts, however written": {func(z *zone) {
			z.mx["bounce.mail.example.com"] = []string{"MX1.Provider.Example.", "provider.example"}
		}, driver.UseBounce, dnscheck.OK},
		"two spf records": {func(z *zone) {
			z.txt["bounce.mail.example.com"] = []string{"v=spf1 include:other.example -all", "v=spf1 include:_spf.provider.example ~all"}
		}, driver.UseSPF, dnscheck.Wrong},
		"two dmarc records":      {func(z *zone) { z.txt["_dmarc.mail.example.com"] = []string{"v=DMARC1; p=none", "v=DMARC1; p=reject"} }, dnscheck.UseDMARC, dnscheck.Wrong},
		"a dmarc with no policy": {func(z *zone) { z.txt["_dmarc.mail.example.com"] = []string{"v=DMARC1; rua=mailto:a@example.com"} }, dnscheck.UseDMARC, dnscheck.Wrong},
		"a server that fails":    {func(z *zone) { z.down = map[string]bool{"sel._domainkey.mail.example.com": true} }, driver.UseDKIM, dnscheck.Unknown},
		"dmarc's server fails":   {func(z *zone) { z.down = map[string]bool{"_dmarc.mail.example.com": true} }, dnscheck.UseDMARC, dnscheck.Unknown},
	} {
		z := zone{txt: map[string][]string{}, mx: map[string][]string{}}
		for k, v := range good.txt {
			z.txt[k] = v
		}
		for k, v := range good.mx {
			z.mx[k] = v
		}
		c.change(&z)
		got := check(t, z, "mail.example.com")
		if got[c.use].State != c.want {
			t.Errorf("%s: %s is %s, want %s", what, c.use, got[c.use].State, c.want)
		}
		for use, r := range got {
			if use != c.use && r.State != dnscheck.OK {
				t.Errorf("%s: %s is %s too", what, use, r.State)
			}
		}
	}
}

// A domain with no DMARC record of its own is spoken for by a parent's,
// whose "sp" tag counts for it when set. None at all is allowed.
func TestDMARCMayComeFromAParent(t *testing.T) {
	z := zone{txt: map[string][]string{
		"_dmarc.example.com":      {"v=DMARC1; p=reject; sp=quarantine"},
		"_dmarc.mail.example.com": {"v=DMARC1; p=none"},
	}}
	d := check(t, z, "a.mail.example.com")[dnscheck.UseDMARC]
	if d.State != dnscheck.OK || d.Inherited != "example.com" || d.Policy != "quarantine" || d.Name != "_dmarc.a.mail.example.com" {
		t.Fatalf("the organisational domain's, not the nearest parent's: %+v", d)
	}
	z.txt["_dmarc.example.com"] = []string{"v=DMARC1; p=reject"}
	if d = check(t, z, "a.mail.example.com")[dnscheck.UseDMARC]; d.Policy != "reject" {
		t.Fatalf("with no sp, p counts: %+v", d)
	}
	if d = check(t, z, "mail.example.com")[dnscheck.UseDMARC]; d.Inherited != "" || d.Policy != "none" {
		t.Fatalf("its own record comes first: %+v", d)
	}
	if d = check(t, zone{}, "mail.example.com")[dnscheck.UseDMARC]; d.State != dnscheck.Missing || d.Policy != "" {
		t.Fatalf("none anywhere: %+v", d)
	}
}

// A resolver that does not answer leaves everything unknown, within the
// check's own deadline; what DNS says is cut to what can be shown.
func TestTheCheckIsBoundedInTimeAndSize(t *testing.T) {
	began := time.Now()
	got := check(t, zone{slow: time.Minute}, "mail.example.com")
	if time.Since(began) > 2*time.Second {
		t.Fatalf("the check took %s", time.Since(began))
	}
	for use, r := range got {
		if r.State != dnscheck.Unknown {
			t.Errorf("%s: %s", use, r.State)
		}
	}
	many := make([]string, 50)
	for i := range many {
		many[i] = "v=spf1 " + strings.Repeat("a", 5000) + "\xff"
	}
	r := check(t, zone{txt: map[string][]string{"bounce.mail.example.com": many}}, "mail.example.com")[driver.UseSPF]
	if len(r.Found) != 8 || len(r.Found[0]) > 310 || !strings.HasSuffix(r.Found[0], "...") {
		t.Fatalf("%d answers, the first %d bytes", len(r.Found), len(r.Found[0]))
	}
}

func TestOnlyAHostNameIsLookedUp(t *testing.T) {
	c := &dnscheck.Checker{Resolver: zone{}, Timeout: time.Second}
	for _, bad := range []string{"", "localhost", "a@b.example", "example.com.", "exa mple.com", "example.com/x", "-a.example.com",
		"a..example.com", strings.Repeat("a", 64) + ".example.com", strings.Repeat("a.", 130) + "com", "ex\x00ample.com", "пример.example"} {
		if _, err := c.Check(context.Background(), bad, nil); !errors.Is(err, dnscheck.ErrNotAHost) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// An include is the same include with a qualifier before it, a dot
// after it, or in capitals.
func TestAnIncludeIsReadAsReceiversReadIt(t *testing.T) {
	for record, want := range map[string]string{
		"v=spf1 +include:_spf.provider.example ~all":  dnscheck.OK,
		"v=spf1 include:_spf.provider.example. ~all":  dnscheck.OK,
		"v=spf1 ?INCLUDE:_SPF.Provider.Example -all":  dnscheck.OK,
		"v=spf1 a mx include:_spf.provider.example":   dnscheck.OK,
		"v=spf1 include:x_spf.provider.example ~all":  dnscheck.Wrong,
		"v=spf1 redirect=_spf.provider.example":       dnscheck.Wrong,
		"v=spf1 exists:include:_spf.provider.example": dnscheck.Wrong,
	} {
		z := zone{txt: map[string][]string{"bounce.mail.example.com": {record}}}
		if got := check(t, z, "mail.example.com")[driver.UseSPF].State; got != want {
			t.Errorf("%q: %s", record, got)
		}
	}
}

// What a parent says of DMARC is read as receivers read it, and a
// parent that could not be asked is never taken for one with no record.
func TestParentsAreReadAsReceiversReadThem(t *testing.T) {
	dmarc := func(z zone, domain string) dnscheck.Result { return check(t, z, domain)[dnscheck.UseDMARC] }
	// A registry's record, marked so, speaks for nobody below it.
	z := zone{txt: map[string][]string{
		"_dmarc.gov.example":        {"v=DMARC1; p=reject; psd=y"},
		"_dmarc.agency.gov.example": {"v=DMARC1; p=none"},
	}}
	if d := dmarc(z, "mail.agency.gov.example"); d.Inherited != "agency.gov.example" || d.Policy != "none" {
		t.Errorf("under a registry's record: %+v", d)
	}
	delete(z.txt, "_dmarc.agency.gov.example")
	if d := dmarc(z, "mail.agency.gov.example"); d.State != dnscheck.Missing || d.Inherited != "" {
		t.Errorf("a registry's record alone: %+v", d)
	}
	// An organisation that says it is one wins over a shorter name.
	z = zone{txt: map[string][]string{
		"_dmarc.example.com":      {"v=DMARC1; p=reject"},
		"_dmarc.team.example.com": {"v=DMARC1; p=quarantine; psd=n"},
	}}
	if d := dmarc(z, "a.b.team.example.com"); d.Inherited != "team.example.com" || d.Policy != "quarantine" {
		t.Errorf("under a record marked psd=n: %+v", d)
	}
	// A parent's record that cannot be read is passed over, and is no
	// fault of the subdomain's.
	z = zone{txt: map[string][]string{"_dmarc.example.com": {"v=DMARC1; rua=mailto:a@example.com"}}}
	if d := dmarc(z, "mail.example.com"); d.State != dnscheck.Missing || d.Inherited != "" {
		t.Errorf("under a parent's record with no policy: %+v", d)
	}
	z.txt["_dmarc.example.com"] = []string{"v=DMARC1; p=none", "v=DMARC1; p=reject"}
	if d := dmarc(z, "mail.example.com"); d.State != dnscheck.Missing {
		t.Errorf("under a parent with two records: %+v", d)
	}
	// A parent that could not be asked leaves it unknown, unless a
	// record was found without it.
	z = zone{down: map[string]bool{"_dmarc.example.com": true}}
	if d := dmarc(z, "mail.example.com"); d.State != dnscheck.Unknown {
		t.Errorf("a parent that could not be asked: %+v", d)
	}
	z.txt = map[string][]string{"_dmarc.mail.example.com": {"v=DMARC1; p=none"}}
	if d := dmarc(z, "mail.example.com"); d.State != dnscheck.OK || d.Policy != "none" {
		t.Errorf("its own record, with a parent that could not be asked: %+v", d)
	}
	// However many labels a name has, only a few parents are asked.
	var asked atomic.Int64
	check(t, zone{asked: &asked}, strings.Repeat("a.", 120)+"example.com")
	if n := asked.Load(); n > 8 {
		t.Errorf("%d lookups for one check", n)
	}
}
