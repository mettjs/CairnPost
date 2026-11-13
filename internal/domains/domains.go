// Package domains is the sending domains a server knows and whether
// each one's DNS is ready to be sent from.
package domains

import (
	"context"
	"errors"
	"fmt"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/dnscheck"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/store"
)

// Most is the most domains a list answers.
const Most = 500

// Service is the domain use cases.
type Service struct {
	store    store.Domains
	checker  *dnscheck.Checker
	records  func(domain string) []driver.Record
	provider string
}

// New returns the service. The records asked of a domain are the
// provider's, if it names any: records is nil for one that does not.
func New(s store.Domains, c *dnscheck.Checker, provider string, records func(string) []driver.Record) *Service {
	return &Service{store: s, checker: c, provider: provider, records: records}
}

// List answers the sending domains an admin key manages, in order of
// name, and whether there are more than it answers. A key bound to a
// domain manages that one, known yet or not.
func (s *Service) List(ctx context.Context, bound string) ([]store.SendingDomain, bool, error) {
	if bound != "" {
		d, err := s.store.SendingDomain(ctx, bound)
		if errors.Is(err, store.ErrNotFound) {
			d, err = store.SendingDomain{Name: bound}, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("domains: read: %w", err)
		}
		return []store.SendingDomain{d}, false, nil
	}
	list, err := s.store.SendingDomains(ctx, Most+1)
	if err != nil {
		return nil, false, fmt.Errorf("domains: list: %w", err)
	}
	if len(list) > Most {
		return list[:Most], true, nil
	}
	return list, false, nil
}

// Report is what a check of a domain's DNS found.
type Report struct {
	Domain string
	// Provider is who sends this server's mail; Asks says it asks DNS
	// records of a sending domain. One that asks none is checked for
	// DMARC alone.
	Provider string
	Asks     bool
	Results  []dnscheck.Result
}

// allowed says a result that is not "in place" still stands in nobody's
// way: a domain may have no DMARC record. It is the one place that says
// so.
func allowed(res dnscheck.Result) bool {
	return res.Use == dnscheck.UseDMARC && res.State == dnscheck.Missing
}

// Wanting is how many records need a look before the domain is sent
// from: those not in place, but for what is allowed.
func (r Report) Wanting() int {
	n := 0
	for _, res := range r.Results {
		if res.State != dnscheck.OK && !allowed(res) {
			n++
		}
	}
	return n
}

// Ready says nothing is wanting.
func (r Report) Ready() bool { return r.Wanting() == 0 }

// NoPolicy says the domain has no DMARC policy, its own or a parent's.
func (r Report) NoPolicy() bool {
	for _, res := range r.Results {
		if allowed(res) {
			return true
		}
	}
	return false
}

// Row is one record of a report: what was found, and what that means.
type Row struct {
	dnscheck.Result
	Words Words
}

// At is the name the record was read at: a DMARC policy taken from a
// parent domain is at the parent's name, not at the domain's own.
func (r Row) At() string {
	if r.Inherited != "" {
		return "_dmarc." + r.Inherited
	}
	return r.Name
}

// Rows is the report record by record, each with its words.
func (r Report) Rows() []Row {
	out := make([]Row, 0, len(r.Results))
	for _, res := range r.Results {
		out = append(out, Row{res, WordsFor(res)})
	}
	return out
}

// ErrNotAHost is a check asked of what is no host name.
var ErrNotAHost = dnscheck.ErrNotAHost

// Host answers a domain as it is looked up, and whether it is one that
// can be: a name from a message's sender has only passed an address
// parser.
func Host(domain string) (string, bool) { return address.Host(domain) }

// Check looks up a domain's records. A key bound to a domain checks that
// one: another is, to it, store.ErrNotFound. A key bound to none checks
// any host name, so that a domain can be checked before its first email.
// ErrNotAHost for what is no host name.
func (s *Service) Check(ctx context.Context, bound, name string) (Report, error) {
	domain, ok := Host(name)
	switch {
	case !ok:
		return Report{}, ErrNotAHost
	case bound != "" && domain != bound:
		return Report{}, store.ErrNotFound
	}
	r := Report{Domain: domain, Provider: s.provider}
	var asked []driver.Record
	if s.records != nil {
		asked = s.records(domain)
	}
	r.Asks = len(asked) > 0
	var err error
	r.Results, err = s.checker.Check(ctx, domain, asked)
	return r, err
}
