package domains

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/driver"
)

// empty is a resolver with nothing in it, which counts what it is asked.
type empty struct{ asked chan string }

func (e empty) LookupTXT(_ context.Context, name string) ([]string, error) {
	e.asked <- name
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (e empty) LookupMX(context.Context, string) ([]*net.MX, error) { return nil, nil }

func one(domain string) []driver.Record {
	return []driver.Record{{Use: driver.UseDKIM, Name: "k._domainkey." + domain, Type: "TXT"}}
}

// What was found is kept for its time and asked again after it, and the
// domains kept are bounded: with as many fresh as are kept, one more is
// looked up and not kept, and none of the others is forgotten for it.
func TestWhatWasFoundIsKeptForItsTimeAndWithinBounds(t *testing.T) {
	e := empty{asked: make(chan string, 4*foundMost)}
	svc := New(nil, &dnscheck.Checker{Resolver: e, Timeout: time.Second}, "test", one)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.seen.now = func() time.Time { return at }
	ctx := context.Background()
	ask := func(domain string) int {
		before := len(e.asked)
		if _, bad := svc.NotSetUp(ctx, domain); !bad {
			t.Fatalf("%s: not called not set up", domain)
		}
		return len(e.asked) - before
	}
	if ask("a.example.com") != 1 || ask("a.example.com") != 0 {
		t.Fatal("a second question within the time read DNS again, or the first did not")
	}
	at = at.Add(foundFor)
	if ask("a.example.com") != 1 {
		t.Fatal("after its time the answer was not asked for again")
	}
	for i := range foundMost - 1 {
		ask(fmt.Sprintf("d%d.example.com", i))
	}
	if ask("over.example.com") != 1 || ask("over.example.com") != 1 {
		t.Fatal("a domain beyond the bound was kept")
	}
	if ask("a.example.com") != 0 || len(svc.seen.known) != foundMost {
		t.Fatalf("a kept domain was forgotten for a new one: %d kept", len(svc.seen.known))
	}
	at = at.Add(foundFor)
	if ask("over.example.com") != 1 || ask("over.example.com") != 0 {
		t.Fatal("once the others had their time, a new domain was still not kept")
	}
}
