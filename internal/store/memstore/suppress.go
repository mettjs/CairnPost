package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// suppression is a row of the list: lifted is nil while it is in force.
type suppression struct {
	store.Suppression
	lifted *time.Time
}

func slot(domain, address string) string { return store.Fold(domain) + "\x00" + store.Fold(address) }

// inForce says whether a suppression holds now. The lock is held.
func (s *Store) inForce(domain, address string) bool {
	row, had := s.suppressed[slot(domain, address)]
	return had && row.lifted == nil
}

// suppress puts a suppression in force unless one is, and says whether it
// did. One that was lifted comes back if revive says so of the lift's
// time. The lock is held.
func (s *Store) suppress(sup store.Suppression, revive func(lifted time.Time) bool) bool {
	key := slot(sup.Domain, sup.Address)
	if row, had := s.suppressed[key]; had && (row.lifted == nil || !revive(*row.lifted)) {
		return false
	}
	sup.Domain, sup.Address, sup.CreatedAt = store.Fold(sup.Domain), store.Fold(sup.Address), s.Now()
	s.suppressed[key] = &suppression{Suppression: sup}
	return true
}

// Suppress implements store.Suppressions.
func (s *Store) Suppress(_ context.Context, sup store.Suppression) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suppress(sup, func(time.Time) bool { return true }), nil
}

// Unsuppress implements store.Suppressions.
func (s *Store) Unsuppress(_ context.Context, domain, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inForce(domain, address) {
		return store.ErrNotFound
	}
	now := s.Now()
	s.suppressed[slot(domain, address)].lifted = &now
	return nil
}

// UnsuppressAll implements store.Suppressions.
func (s *Store) UnsuppressAll(_ context.Context, domain, reason string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, now := 0, s.Now()
	for _, row := range s.suppressed {
		if row.lifted == nil && row.Domain == store.Fold(domain) && (reason == "" || row.Reason == reason) {
			row.lifted = &now
			n++
		}
	}
	return n, nil
}

// ListSuppressions implements store.Suppressions.
func (s *Store) ListSuppressions(_ context.Context, domain string, after *store.Suppression, limit int) ([]store.Suppression, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Suppression
	for _, row := range s.suppressed {
		past := after == nil || row.Domain > after.Domain || (row.Domain == after.Domain && row.Address > after.Address)
		if row.lifted == nil && (domain == "" || row.Domain == store.Fold(domain)) && past {
			out = append(out, row.Suppression)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Domain < out[j].Domain || (out[i].Domain == out[j].Domain && out[i].Address < out[j].Address)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SuppressionsOf implements store.Suppressions.
func (s *Store) SuppressionsOf(_ context.Context, domain, address string) ([]store.Suppression, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Suppression
	domain, address = store.Fold(domain), store.Fold(address)
	for _, row := range s.suppressed {
		if row.lifted == nil && row.Address == address && (domain == "" || row.Domain == domain) {
			out = append(out, row.Suppression)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}
