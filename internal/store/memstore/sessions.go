package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// session is a console session: whose it is and when it ends.
type session struct {
	keyID            string
	lookups          []lookup
	started, expires time.Time
}

// CreateSession implements store.Sessions.
func (s *Store) CreateSession(_ context.Context, hash []byte, keyID string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[string(hash)] = session{keyID: keyID, started: s.Now(), expires: s.Now().Add(ttl)}
	return nil
}

// SessionKey implements store.Sessions.
func (s *Store) SessionKey(_ context.Context, hash []byte, ttl time.Duration) (store.Key, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.sessions[string(hash)]
	if latest := held.started.Add(ttl); latest.Before(held.expires) {
		held.expires = latest
	}
	if !ok || !held.expires.After(s.Now()) {
		return store.Key{}, time.Time{}, store.ErrNotFound
	}
	for _, k := range s.keys {
		if k.ID == held.keyID && k.RevokedAt == nil {
			return k, held.expires, nil
		}
	}
	return store.Key{}, time.Time{}, store.ErrNotFound
}

// lookup is an address a session looks for, under its handle.
type lookup struct {
	handle, address string
	used            int // the order it was last looked for in
}

// Remember implements store.Sessions.
func (s *Store) Remember(_ context.Context, hash []byte, handle, address string, keep int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.sessions[string(hash)]
	if !ok {
		return "", nil
	}
	s.seq++
	at := slices.IndexFunc(held.lookups, func(l lookup) bool { return l.address == address })
	if at < 0 {
		held.lookups = append(held.lookups, lookup{handle: handle, address: address})
		at = len(held.lookups) - 1
	}
	held.lookups[at].used = s.seq
	kept := held.lookups[at].handle
	// The ones looked for last stay.
	slices.SortFunc(held.lookups, func(a, b lookup) int { return b.used - a.used })
	if len(held.lookups) > keep {
		held.lookups = held.lookups[:keep]
	}
	s.sessions[string(hash)] = held
	return kept, nil
}

// Recall implements store.Sessions.
func (s *Store) Recall(_ context.Context, hash []byte, handle string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.sessions[string(hash)]
	for i, l := range held.lookups {
		if l.handle == handle {
			// Asked for is looked for: it is not the one that goes next.
			s.seq++
			held.lookups[i].used = s.seq
			return l.address, nil
		}
	}
	return "", nil
}

// Forget implements store.Sessions.
func (s *Store) Forget(_ context.Context, hash []byte, handle string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, ok := s.sessions[string(hash)]; ok {
		held.lookups = slices.DeleteFunc(held.lookups, func(l lookup) bool { return l.handle == handle })
		s.sessions[string(hash)] = held
	}
	return nil
}

// DeleteSession implements store.Sessions.
func (s *Store) DeleteSession(_ context.Context, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, string(hash))
	return nil
}

// PruneSessions implements store.Pruning.
func (s *Store) PruneSessions(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, now := 0, s.Now()
	for hash, held := range s.sessions {
		if n < limit && !held.expires.After(now) {
			delete(s.sessions, hash)
			n++
		}
	}
	return n, nil
}
