package memstore

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
)

// inbound is an email that came, with what it was as it came.
type inbound struct {
	store.Inbound
	digest, raw []byte
}

// shownInbound is one that is kept and whose time is not up. The lock
// is held.
func (s *Store) shownInbound(id string) (*inbound, bool) {
	in, ok := s.inbounds[id]
	return in, ok && in.ExpiresAt.After(s.Now())
}

// dropInbound deletes one with what was owed for it. The lock is held.
func (s *Store) dropInbound(id string) {
	delete(s.inbounds, id)
	s.deliveries = slices.DeleteFunc(s.deliveries, func(d *delivery) bool { return d.came != nil && d.came.ID == id })
}

// Receive implements store.Inbounds.
func (s *Store) Receive(_ context.Context, n store.NewInbound) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, held := s.Now(), int64(0)
	for id, in := range s.inbounds {
		same := in.Recipient == n.Recipient && bytes.Equal(in.digest, n.Digest)
		switch {
		case same && in.ExpiresAt.After(now):
			return id, true, nil
		case same:
			s.dropInbound(id) // its time is up: no longer there to be the same as
		case in.ExpiresAt.After(now):
			held += int64(in.Bytes)
		}
	}
	if n.Room != 0 && held+int64(len(n.Raw)) > n.Room {
		return "", false, store.ErrLimit
	}
	in := &inbound{Inbound: n.Inbound, digest: bytes.Clone(n.Digest), raw: bytes.Clone(n.Raw)}
	in.ID, in.Bytes, in.ReceivedAt, in.ExpiresAt = s.next("in"), len(n.Raw), now, now.Add(n.Keep)
	s.inbounds[in.ID] = in
	for _, end := range s.endpoints {
		if end.Enabled && (end.Domain == "" || end.Domain == in.Domain) && (len(end.Kinds) == 0 || slices.Contains(end.Kinds, store.KindReceived)) {
			came := in.Inbound
			s.deliveries = append(s.deliveries, &delivery{came: &came, Delivery: store.Delivery{ID: s.next("dlv"),
				EndpointID: end.ID, Kind: store.KindReceived, InboundID: in.ID, State: store.DeliveryPending, NextAt: now, CreatedAt: now}})
		}
	}
	return in.ID, false, nil
}

// Inbound implements store.Inbounds.
func (s *Store) Inbound(_ context.Context, id string) (store.Inbound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.shownInbound(id)
	if !ok {
		return store.Inbound{}, store.ErrNotFound
	}
	return in.Inbound, nil
}

// InboundRaw implements store.Inbounds.
func (s *Store) InboundRaw(_ context.Context, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.shownInbound(id)
	if !ok {
		return nil, store.ErrNotFound
	}
	return bytes.Clone(in.raw), nil
}

// ListInbound implements store.Inbounds. Ids are made in order, so
// "before" is "made before".
func (s *Store) ListInbound(_ context.Context, domain, before string, limit int) ([]store.Inbound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if before != "" && !strings.HasPrefix(before, "in-") {
		return nil, nil // an id no such email could have, as in the database
	}
	var out []store.Inbound
	for id, in := range s.inbounds {
		if _, shown := s.shownInbound(id); shown && (domain == "" || in.Domain == domain) && (before == "" || id < before) {
			out = append(out, in.Inbound)
		}
	}
	slices.SortFunc(out, func(a, b store.Inbound) int { return strings.Compare(b.ID, a.ID) })
	return out[:min(len(out), limit)], nil
}

// DeleteInbound implements store.Inbounds.
func (s *Store) DeleteInbound(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.inbounds[id]; !ok {
		return store.ErrNotFound
	}
	s.dropInbound(id)
	return nil
}

// CountInbound implements store.Inbounds.
func (s *Store) CountInbound(_ context.Context, domain string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, in := range s.inbounds {
		if _, shown := s.shownInbound(id); shown && (domain == "" || in.Domain == domain) {
			n++
		}
	}
	return n, nil
}

// PruneInbound implements store.Pruning.
func (s *Store) PruneInbound(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id := range s.inbounds {
		if _, shown := s.shownInbound(id); !shown && n < limit {
			s.dropInbound(id)
			n++
		}
	}
	return n, nil
}
