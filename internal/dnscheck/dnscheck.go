// Package dnscheck looks up the DNS records a sending domain needs and
// says, for each, whether it is there. It reads public DNS and nothing
// else: it never asks the mail provider, and it cannot know a provider's
// own values, so "in place" means found and well formed, as the resolver
// this server uses sees it.
package dnscheck

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/driver"
)

// UseDMARC is the record that says what receivers do with mail that
// fails the other checks. It is asked of every domain, whatever the
// upstream.
const UseDMARC = "dmarc"

// What a lookup found.
const (
	// OK: the record is there and well formed.
	OK = "ok"
	// Missing: the name has no such record. For DMARC, none is allowed.
	Missing = "missing"
	// Wrong: something is there, and it is not what is needed.
	Wrong = "wrong"
	// Unknown: the lookup failed; nothing is known either way.
	Unknown = "unknown"
)

// States is every state a result can be in.
var States = []string{OK, Missing, Wrong, Unknown}

// Result is what was found for one record.
type Result struct {
	driver.Record
	State string
	// Found is what DNS answered, cut to a length that can be shown. It
	// is someone else's text: shown as text, never logged.
	Found []string
	// Inherited is, for DMARC, the parent domain whose record was found
	// when the domain has none of its own; "" otherwise. Policy is what
	// that record tells receivers to do: "none", "quarantine" or
	// "reject"; "" when it says none that can be read.
	Inherited, Policy string
}

// Resolver is the part of a DNS resolver the check uses.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// Checker looks records up.
type Checker struct {
	Resolver Resolver
	// Timeout bounds one whole check.
	Timeout time.Duration
}

// New returns a checker; one whole check may take timeout. It asks the
// system's resolver, or, when server is not "", the DNS server at that
// address ("1.1.1.1:53") and no other: for a machine whose own resolver
// does not answer as the public internet does.
func New(timeout time.Duration, server string) *Checker {
	if server == "" {
		return &Checker{Resolver: net.DefaultResolver, Timeout: timeout}
	}
	dialer := &net.Dialer{Timeout: timeout}
	return &Checker{Timeout: timeout, Resolver: &net.Resolver{PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, server)
		}}}
}

// under reports whether a host is a name, or below it.
func under(host, name string) bool {
	host, name = strings.ToLower(strings.Trim(host, ".")), strings.ToLower(strings.Trim(name, "."))
	return host == name || strings.HasSuffix(host, "."+name)
}

// ErrNotAHost is a check asked of what is no host name.
var ErrNotAHost = errors.New("dnscheck: not a host name")

// rooted is a name as it is asked of DNS: whole, so that a resolver
// never tries it under the search domains of the machine it runs on.
func rooted(name string) string { return name + "." }

// Check looks up the records an upstream asks of a domain, and its
// DMARC record, at once and under one deadline. A lookup that fails
// leaves its result Unknown: the check itself does not fail.
func (c *Checker) Check(ctx context.Context, domain string, records []driver.Record) ([]Result, error) {
	domain, ok := address.Host(domain)
	if !ok {
		return nil, ErrNotAHost
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	out := make([]Result, len(records)+1)
	var wg sync.WaitGroup
	for i, r := range records {
		wg.Go(func() { out[i] = c.one(ctx, r) })
	}
	wg.Go(func() { out[len(records)] = c.dmarc(ctx, domain) })
	wg.Wait()
	return out, nil
}

// one looks one record up.
func (c *Checker) one(ctx context.Context, r driver.Record) Result {
	res := Result{Record: r}
	if r.Type == "MX" {
		hosts, err := c.Resolver.LookupMX(ctx, rooted(r.Name))
		good := len(hosts) > 0
		for _, h := range hosts {
			res.Found = append(res.Found, h.Host)
			// "." is the null MX: the name says it takes no mail. A host
			// that is not the upstream's takes the bounces somewhere the
			// upstream never hears of them: a wildcard record, or another
			// provider's, answers there too.
			good = good && strings.Trim(h.Host, ".") != "" && (r.Under == "" || under(h.Host, r.Under))
		}
		res.State, res.Found = stateOf(err, len(hosts) > 0, good), shown(res.Found)
		return res
	}
	texts, err := c.Resolver.LookupTXT(ctx, rooted(r.Name))
	var good bool
	switch r.Use {
	case driver.UseDKIM:
		texts, good = pick(texts, func(t string) bool { return tag(t, "p") != "" })
	case driver.UseSPF:
		// More than one SPF record at a name is an error to a receiver,
		// which then applies none of them.
		texts = only(texts, "v=spf1")
		good = len(texts) == 1 && (r.Include == "" || includes(texts[0], r.Include))
	}
	res.State, res.Found = stateOf(err, len(texts) > 0, good), shown(texts)
	return res
}

// stateOf turns a lookup's outcome into a state. No such name, and a name
// with no record of the kind, are Missing; any other failure is Unknown.
func stateOf(err error, any, good bool) string {
	var dns *net.DNSError
	switch {
	case good:
		return OK
	case any:
		return Wrong
	case err == nil, errors.As(err, &dns) && dns.IsNotFound:
		return Missing
	}
	return Unknown
}
