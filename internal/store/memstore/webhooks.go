package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

func (s *Store) endpoint(id string) *store.Endpoint {
	for _, e := range s.endpoints {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// CreateEndpoint implements store.Webhooks.
func (s *Store) CreateEndpoint(_ context.Context, e store.Endpoint, max int) (store.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Domain = store.Fold(e.Domain)
	same := 0
	for _, had := range s.endpoints {
		if had.Domain == e.Domain {
			same++
		}
	}
	if same >= max {
		return store.Endpoint{}, store.ErrLimit
	}
	e.ID, e.Enabled, e.CreatedAt = s.next("wh"), true, s.Now()
	e.Kinds = slices.Clone(e.Kinds)
	s.endpoints = append(s.endpoints, &e)
	return e, nil
}

// Endpoint implements store.Webhooks.
func (s *Store) Endpoint(_ context.Context, id string) (store.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.endpoint(id); e != nil {
		return s.shown(e), nil
	}
	return store.Endpoint{}, store.ErrNotFound
}

// ListEndpoints implements store.Webhooks.
func (s *Store) ListEndpoints(_ context.Context, domain string) ([]store.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Endpoint
	for _, e := range s.endpoints {
		if domain == "" || e.Domain == store.Fold(domain) {
			out = append(out, s.shown(e))
		}
	}
	return out, nil
}

// UpdateEndpoint implements store.Webhooks.
func (s *Store) UpdateEndpoint(_ context.Context, id string, c store.EndpointChange) (store.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.endpoint(id)
	if e == nil {
		return store.Endpoint{}, store.ErrNotFound
	}
	if c.URL != nil {
		e.URL = *c.URL
	}
	if c.Kinds != nil {
		e.Kinds = slices.Clone(*c.Kinds)
	}
	if c.Description != nil {
		e.Description = *c.Description
	}
	if c.Enabled != nil {
		e.Enabled = *c.Enabled
	}
	return s.shown(e), nil
}

// before is a signing key an endpoint had, and until when it signs.
type before struct {
	secret []byte
	until  time.Time
}

// shown is an endpoint as it is answered: with the keys it had before
// that still sign, the last changed first. The lock is held.
func (s *Store) shown(e *store.Endpoint) store.Endpoint {
	out := *e
	out.Previous, out.PreviousUntil = nil, nil
	keys := slices.Clone(s.previous[e.ID])
	slices.SortStableFunc(keys, func(a, b before) int { return b.until.Compare(a.until) })
	for _, k := range keys {
		if k.until.After(s.Now()) && len(out.Previous) < store.PreviousKept {
			if out.PreviousUntil == nil {
				until := k.until
				out.PreviousUntil = &until
			}
			out.Previous = append(out.Previous, k.secret)
		}
	}
	return out
}

// RotateSecret implements store.Webhooks.
func (s *Store) RotateSecret(_ context.Context, id string, secret []byte, overlap time.Duration) (store.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.endpoint(id)
	if e == nil {
		return store.Endpoint{}, store.ErrNotFound
	}
	if overlap > 0 {
		if s.previous == nil {
			s.previous = map[string][]before{}
		}
		s.previous[id] = append(s.previous[id], before{e.Secret, s.Now().Add(overlap)})
	}
	e.Secret = secret
	return s.shown(e), nil
}

// ResumeEndpoint implements store.Webhooks.
func (s *Store) ResumeEndpoint(_ context.Context, id string) (store.Endpoint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.endpoint(id)
	if e == nil {
		return store.Endpoint{}, false, store.ErrNotFound
	}
	if e.PausedAt == nil {
		return s.shown(e), false, nil
	}
	// What was given up while it failed is owed again.
	for _, d := range s.deliveries {
		if d.EndpointID == id && givenUp(e, d) {
			d.State, d.NextAt = store.DeliveryPending, s.Now()
		}
	}
	e.FailingSince, e.PausedAt = nil, nil
	return s.shown(e), true, nil
}

// givenUp reports whether a delivery of an endpoint is owed again when
// the endpoint is resumed: given up since the failing that paused it.
func givenUp(e *store.Endpoint, d *delivery) bool {
	return e.PausedAt != nil && d.State == store.DeliveryFailed && d.AttemptedAt != nil && !d.AttemptedAt.Before(*e.FailingSince)
}

// Owed implements store.Webhooks.
func (s *Store) Owed(_ context.Context, ids []string, most int) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, d := range s.deliveries {
		e := s.endpoint(d.EndpointID)
		if e != nil && slices.Contains(ids, e.ID) && (d.State == store.DeliveryPending || givenUp(e, d)) {
			out[e.ID] = min(out[e.ID]+1, most)
		}
	}
	return out, nil
}

// EndpointAnswered implements store.Webhooks.
func (s *Store) EndpointAnswered(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.endpoint(id); e != nil && e.PausedAt == nil {
		e.FailingSince = nil
	}
	return nil
}

// DeleteEndpoint implements store.Webhooks.
func (s *Store) DeleteEndpoint(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint(id) == nil {
		return store.ErrNotFound
	}
	s.endpoints = slices.DeleteFunc(s.endpoints, func(e *store.Endpoint) bool { return e.ID == id })
	s.deliveries = slices.DeleteFunc(s.deliveries, func(d *delivery) bool { return d.EndpointID == id })
	delete(s.previous, id)
	return nil
}
