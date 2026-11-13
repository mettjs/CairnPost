package suppress

import (
	"context"
	"errors"
	"fmt"

	"github.com/mettjs/cairnpost/internal/store"
)

// ErrDomain is a key bound to one sending domain reaching for another's
// suppressions.
var ErrDomain = errors.New("suppress: this key manages only the suppressions of its own sending domain")

// Scoped is the service as one admin key may use it: a key bound to a
// sending domain manages that domain's suppressions and no other's. The
// rule is kept here, so that every way in keeps it.
type Scoped struct {
	s *Service
	// bound is the key's sending domain, "" for a key bound to none.
	bound string
}

// For answers the service as a key bound to a sending domain may use it;
// "" is a key bound to none, which may use all of it.
func (s *Service) For(bound string) Scoped { return Scoped{s, bound} }

// domain is the domain a call acts on: the one asked for, which for a
// bound key may only be its own. A bound key that names none gets its own.
func (v Scoped) domain(asked string) (string, error) {
	switch {
	case v.bound == "":
		return asked, nil
	case asked == "" || store.Fold(asked) == v.bound:
		return v.bound, nil
	}
	return "", ErrDomain
}

// Add is Service.Add for the key.
func (v Scoped) Add(ctx context.Context, domain, addr, note string) (store.Suppression, bool, error) {
	domain, err := v.domain(domain)
	if err != nil {
		return store.Suppression{}, false, err
	}
	return v.s.Add(ctx, domain, addr, note)
}

// Lift is Service.Lift for the key.
func (v Scoped) Lift(ctx context.Context, domain, addr string) error {
	domain, err := v.domain(domain)
	if err != nil {
		return err
	}
	return v.s.Lift(ctx, domain, addr)
}

// LiftAll is Service.LiftAll for the key.
func (v Scoped) LiftAll(ctx context.Context, domain, reason string) (int, error) {
	domain, err := v.domain(domain)
	if err != nil {
		return 0, err
	}
	return v.s.LiftAll(ctx, domain, reason)
}

// List is Service.List for the key.
func (v Scoped) List(ctx context.Context, domain, cursor string, limit int) ([]store.Suppression, string, error) {
	domain, err := v.domain(domain)
	if err != nil {
		return nil, "", err
	}
	return v.s.List(ctx, domain, cursor, limit)
}

// Domain answers the sending domain a call that names one would act on,
// as the list keeps it: the one asked for, or for a bound key that names
// none its own. "" is every domain, which only a key bound to none gets.
// Its errors are an *Invalid and ErrDomain. It reads nothing.
func (v Scoped) Domain(asked string) (string, error) {
	domain, err := v.domain(asked)
	if err != nil || domain == "" {
		return "", err
	}
	return domainOf(domain)
}

// Of answers the suppressions in force of one address, an address as it
// is sent to: for one sending domain, or for every one the key manages
// (""). It is looked up under the key the send path looks it up under.
func (v Scoped) Of(ctx context.Context, domain, recipient string) ([]store.Suppression, error) {
	domain, err := v.Domain(domain)
	if err != nil {
		return nil, err
	}
	_, address := store.SuppressionKey("", recipient)
	if address == "" || !kept(address) {
		// Nothing the database can hold is kept under such a key.
		return nil, nil
	}
	found, err := v.s.store.SuppressionsOf(ctx, domain, address)
	if err != nil {
		return nil, fmt.Errorf("suppress: find: %w", err)
	}
	return found, nil
}
