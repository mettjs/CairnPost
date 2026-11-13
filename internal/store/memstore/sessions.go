package memstore

import (
	"context"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// session is a console session: whose it is and when it ends.
type session struct {
	keyID, lookingFor string
	started, expires  time.Time
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

// LookFor implements store.Sessions.
func (s *Store) LookFor(_ context.Context, hash []byte, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, ok := s.sessions[string(hash)]; ok {
		held.lookingFor = address
		s.sessions[string(hash)] = held
	}
	return nil
}

// LookingFor implements store.Sessions.
func (s *Store) LookingFor(_ context.Context, hash []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[string(hash)].lookingFor, nil
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
