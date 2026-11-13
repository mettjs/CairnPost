// Package suppress manages the addresses CairnPost will not send to: the
// list, adding one by hand, and lifting those that should not be there.
package suppress

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/store"
)

// Invalid is a request that cannot be carried out as it is. Reason is a
// whole sentence saying what it must be.
type Invalid struct{ Reason string }

func (e *Invalid) Error() string { return "suppress: " + e.Reason }

// Limits of a list and of a suppression.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
	MaxNote      = 500
	maxDomain    = 253
)

// Service is the suppression use cases.
type Service struct{ store store.Suppressions }

// New returns the service.
func New(s store.Suppressions) *Service { return &Service{store: s} }

// plain says whether text can be a key of the list: text the database
// keeps as it is, with nothing in it that hides.
func plain(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// domainOf answers a sending domain as the list keeps it. Any domain an
// email can be sent from must pass, so only what can be no domain at all
// is refused.
func domainOf(domain string) (string, error) {
	// Checked as typed: folding would paper over bytes that are no text.
	ok := plain(domain)
	domain = store.Fold(domain)
	if !ok || domain == "" || len(domain) > maxDomain || strings.ContainsAny(domain, "@ ") {
		return "", &Invalid{`"domain" must be the sending domain the suppression holds for, such as "mail.example.com".`}
	}
	return domain, nil
}

// Add suppresses an address for a sending domain by hand, and says whether
// it was added: false means it was suppressed already, and stays as it was.
func (s *Service) Add(ctx context.Context, domain, addr, note string) (store.Suppression, bool, error) {
	domain, err := domainOf(domain)
	if err != nil {
		return store.Suppression{}, false, err
	}
	bare, ok := address.Bare(addr)
	if !ok {
		return store.Suppression{}, false, &Invalid{`"address" must be a bare email address, such as "ana@example.org".`}
	}
	note = strings.TrimSpace(note)
	if len(note) > MaxNote || !plain(note) {
		return store.Suppression{}, false, &Invalid{fmt.Sprintf(`"note" must be one line of at most %d bytes.`, MaxNote)}
	}
	sup := store.Suppression{Domain: domain, Address: store.Fold(bare), Reason: store.ReasonManual, Note: note}
	added, err := s.store.Suppress(ctx, sup)
	if err != nil {
		return store.Suppression{}, false, fmt.Errorf("suppress: add: %w", err)
	}
	return sup, added, nil
}

// Lift ends a suppression, so the address is sent to again;
// store.ErrNotFound for none in force. If an email sent from now on
// bounces for good, the address is suppressed again.
//
// The address is looked up as an address is kept and, failing that, as it
// was typed: whatever the list shows can be lifted.
func (s *Service) Lift(ctx context.Context, domain, addr string) error {
	domain, err := domainOf(domain)
	if err != nil {
		return err
	}
	typed := store.Fold(addr)
	if typed == "" || !plain(addr) {
		return &Invalid{`"address" must be the address as the list shows it.`}
	}
	tries := []string{typed}
	if bare, ok := address.Bare(addr); ok && store.Fold(bare) != typed {
		tries = []string{store.Fold(bare), typed}
	}
	for _, try := range tries {
		if err = s.store.Unsuppress(ctx, domain, try); err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("suppress: lift: %w", err)
		}
	}
	return store.ErrNotFound
}

// LiftAll ends every suppression of a sending domain, or those of one
// reason ("" for all), and answers how many. It is for the day a fault in
// the domain's setup, or in the provider, suppressed people wrongly.
func (s *Service) LiftAll(ctx context.Context, domain, reason string) (int, error) {
	domain, err := domainOf(domain)
	if err != nil {
		return 0, err
	}
	if reason != "" && !slices.Contains(store.Reasons, reason) {
		return 0, &Invalid{`"reason" must be "bounce", "complaint" or "manual", or left out for all.`}
	}
	n, err := s.store.UnsuppressAll(ctx, domain, reason)
	if err != nil {
		return 0, fmt.Errorf("suppress: lift all: %w", err)
	}
	return n, nil
}
