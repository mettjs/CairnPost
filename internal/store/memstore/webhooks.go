package memstore

import (
	"context"
	"slices"

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
		return *e, nil
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
			out = append(out, *e)
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
	return *e, nil
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
	return nil
}
