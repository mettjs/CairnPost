package mail_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/driver/cloudflare"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// dns answers NotSetUp as a test sets it, and keeps what it was asked and
// the state of the email when it was.
type dns struct {
	wanting bool
	asked   []string
	states  []string
	store   *memstore.Store
}

func (d *dns) NotSetUp(ctx context.Context, domain string) (mail.Record, bool) {
	d.asked = append(d.asked, domain)
	if list, err := d.store.Messages(ctx, store.MessageQuery{Limit: 5}); err == nil && len(list) == 1 {
		d.states = append(d.states, list[0].State)
	}
	return mail.Record{Name: "cf-bounce._domainkey." + domain, Type: "TXT"}, d.wanting
}

func senderRig(t *testing.T, d *dns) *rig {
	t.Helper()
	r := newRig(t)
	if d != nil {
		d.store = r.store
		r.svc = mail.New(r.store, r.drv, mail.Options{SendTimeout: time.Second, Domains: d,
			IdempotencyTTL: 24 * time.Hour, InDoubtWindow: 2 * time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	return r
}

// The upstream's one answer for a bad email and for a domain it does not
// know becomes the domain's when the domain's DNS has none of the records
// the upstream asks; the domain is said folded. It stays an email refused for good to whoever does not
// ask, the send is settled before DNS is read, and its key is free: once
// the domain is set up the same key sends.
func TestAnUnclearRefusalFromADomainNotSetUpIsTheDomains(t *testing.T) {
	d := &dns{wanting: true}
	r := senderRig(t, d)
	r.drv.err = fmt.Errorf("status 400: %w", driver.ErrSenderUnclear)
	req := request("k")
	req.From = "App <app@Example.COM>"
	id, err := r.send(req)
	var sender *mail.SenderDomain
	if !errors.As(err, &sender) || !errors.Is(err, driver.ErrRejected) || sender.Domain != "example.com" || sender.Record.Name != "cf-bounce._domainkey.example.com" {
		t.Fatalf("err %v (%+v)", err, sender)
	}
	if r.state(t, id) != store.StateFailed || len(d.asked) != 1 || d.asked[0] != "example.com" {
		t.Fatalf("state %s, asked %v", r.state(t, id), d.asked)
	}
	if len(d.states) != 1 || d.states[0] != store.StateFailed {
		t.Fatalf("DNS was read before the send was settled: %v", d.states)
	}
	r.drv.err = nil
	if _, err := r.send(req); err != nil || r.drv.sends != 2 {
		t.Fatalf("the same key, once the domain is set up: err %v, sends %d", err, r.drv.sends)
	}
}

// Every other outcome is answered as it came, and DNS is read for none
// but the unclear refusal.
func TestOnlyAnUnclearRefusalAsksAboutTheDomain(t *testing.T) {
	unclear := fmt.Errorf("status 400: %w", driver.ErrSenderUnclear)
	cases := []struct {
		why     string
		dns     *dns
		err     error
		asks    int
		unclear bool
	}{
		{"records in place, or nothing learned: the email's", &dns{}, unclear, 1, true},
		{"a server that does not look", nil, unclear, 0, true},
		{"refused for a reason the upstream said", &dns{wanting: true}, driver.ErrRejected, 0, false},
		{"not taken for now", &dns{wanting: true}, driver.ErrUnavailable, 0, false},
		{"at the limit", &dns{wanting: true}, &driver.Throttled{After: time.Minute}, 0, false},
		{"sent", &dns{wanting: true}, nil, 0, false},
	}
	for _, c := range cases {
		r := senderRig(t, c.dns)
		r.drv.err = c.err
		_, err := r.send(request(""))
		var sender *mail.SenderDomain
		if errors.As(err, &sender) || !errors.Is(err, c.err) || errors.Is(err, driver.ErrSenderUnclear) != c.unclear {
			t.Errorf("%s: err %v", c.why, err)
		}
		if c.dns != nil && len(c.dns.asked) != c.asks {
			t.Errorf("%s: DNS asked %d times, want %d", c.why, len(c.dns.asked), c.asks)
		}
	}
}

// nowhere is a resolver with no record in it.
type nowhere struct{}

func (nowhere) LookupTXT(_ context.Context, name string) ([]string, error) {
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (nowhere) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// The parts as the server joins them: Cloudflare's answer for an invalid
// email, the look at DNS, and the error the API names.
func TestCloudflaresUnclearAnswerFromADomainWithNoRecords(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":10202,"message":"email.sending.error.email.invalid"}],"messages":[],"result":null}`)
	}))
	defer upstream.Close()
	st := memstore.New()
	senders := domains.New(st, &dnscheck.Checker{Resolver: nowhere{}, Timeout: time.Second}, "cloudflare", cloudflare.Records)
	svc := mail.New(st, cloudflare.NewAt(upstream.URL, "token"), mail.Options{SendTimeout: time.Second, Domains: senders,
		IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := svc.Send(context.Background(), store.Key{ID: "key-1", Scope: store.ScopeSend}, "cp_presented", request(""))
	var sender *mail.SenderDomain
	if !errors.As(err, &sender) || sender.Domain != "example.com" || sender.Record != (mail.Record{Name: "cf-bounce._domainkey.example.com", Type: "TXT"}) {
		t.Fatalf("err %v (%+v)", err, sender)
	}
}
