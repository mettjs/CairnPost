package domains

import (
	"context"
	"sync"
	"time"

	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/mail"
)

// How long what NotSetUp found is kept, and for how many domains: a
// domain that is not set up is asked about once for each email it is
// refused, and DNS need not be read for each.
const (
	foundFor  = 30 * time.Second
	foundMost = 256
)

// look is what NotSetUp learned of one domain. While done is open the
// look is under way, and whoever else asks waits for it.
type look struct {
	done   chan struct{}
	record mail.Record
	bad    bool
	until  time.Time
}

// memo keeps the last answers of NotSetUp.
type memo struct {
	mu    sync.Mutex
	known map[string]*look
	now   func() time.Time // nil for the clock
}

func (m *memo) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// NotSetUp says a domain has none of the DNS records the provider asks
// in place: each was looked up and is not there, or is there and is not
// the provider's. That is a domain never set up with the provider. One
// record in place, or one lookup that failed, and it answers false: a
// domain that is set up and has a record awry is not this, and a lookup
// that failed says nothing. It is a narrower question than a report's
// Ready, and DMARC is no part of it: no provider refuses a domain for
// want of a policy.
//
// What it finds is kept for half a minute, "nothing learned" too, and
// those who ask about a domain while it is being looked up share the one
// look.
func (s *Service) NotSetUp(ctx context.Context, domain string) (mail.Record, bool) {
	domain, ok := Host(domain)
	if !ok || s.records == nil {
		return mail.Record{}, false
	}
	asked := s.records(domain)
	if len(asked) == 0 {
		return mail.Record{}, false
	}
	m := &s.seen
	m.mu.Lock()
	f, known := m.known[domain]
	if known && (f.until.IsZero() || m.clock().Before(f.until)) {
		m.mu.Unlock()
		select {
		case <-f.done:
			return f.record, f.bad
		case <-ctx.Done():
			return mail.Record{}, false
		}
	}
	f = &look{done: make(chan struct{})}
	kept := m.room(domain)
	if kept {
		m.known[domain] = f
	}
	m.mu.Unlock()

	results := s.checker.Records(ctx, asked)
	f.bad = true
	for _, res := range results {
		f.bad = f.bad && (res.State == dnscheck.Missing || res.State == dnscheck.Wrong)
	}
	if f.bad {
		f.record = mail.Record{Name: results[0].Name, Type: results[0].Type}
	}
	m.mu.Lock()
	f.until = m.clock().Add(foundFor)
	if kept && ctx.Err() != nil {
		// Cut short by the caller: what it found is the caller's haste,
		// not the domain's state.
		delete(m.known, domain)
	}
	m.mu.Unlock()
	close(f.done)
	return f.record, f.bad
}

// room makes room for a domain and says whether there is any: what has
// had its time goes first, and with foundMost domains still fresh a new
// one is looked up without being kept. Called with the lock held.
func (m *memo) room(domain string) bool {
	if m.known == nil {
		m.known = map[string]*look{}
	}
	delete(m.known, domain)
	if len(m.known) < foundMost {
		return true
	}
	now := m.clock()
	for name, f := range m.known {
		if !f.until.IsZero() && !now.Before(f.until) {
			delete(m.known, name)
		}
	}
	return len(m.known) < foundMost
}
