package memstore

import (
	"context"
	"slices"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
)

// domains gathers every sending domain there is.
func (s *Store) domains() map[string]*store.SendingDomain {
	out := map[string]*store.SendingDomain{}
	at := func(name string) *store.SendingDomain {
		if out[name] == nil {
			out[name] = &store.SendingDomain{Name: name}
		}
		return out[name]
	}
	for _, k := range s.keys {
		if k.Domain != "" && k.RevokedAt == nil {
			at(k.Domain).Keys++
		}
	}
	for _, m := range s.messages {
		d, sent := at(store.DomainOf(m.Sender)), m.CreatedAt
		if d.LastSent == nil || sent.After(*d.LastSent) {
			d.LastSent = &sent
		}
	}
	return out
}

// SendingDomains implements store.Domains.
func (s *Store) SendingDomains(_ context.Context, limit int) ([]store.SendingDomain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.SendingDomain{}
	for _, d := range s.domains() {
		out = append(out, *d)
	}
	slices.SortFunc(out, func(a, b store.SendingDomain) int { return strings.Compare(a.Name, b.Name) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SendingDomain implements store.Domains.
func (s *Store) SendingDomain(_ context.Context, name string) (store.SendingDomain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.domains()[name]; d != nil {
		return *d, nil
	}
	return store.SendingDomain{}, store.ErrNotFound
}
