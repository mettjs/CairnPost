// Package session is who is signed in to the console: it starts a session
// for a key, answers the key a session is of, and ends it. A session is a
// random token held by the browser; what is kept is its hash.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// Service is the session use cases.
type Service struct {
	store store.Sessions
	ttl   time.Duration
}

// New returns the service; a session lasts ttl.
func New(s store.Sessions, ttl time.Duration) *Service { return &Service{store: s, ttl: ttl} }

// TTL is how long a session lasts.
func (s *Service) TTL() time.Duration { return s.ttl }

// hashOf is a token as it is kept.
func hashOf(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Start begins a session of a key and answers its token, the only time
// the token exists here.
func (s *Service) Start(ctx context.Context, key store.Key) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("session: no randomness: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := s.store.CreateSession(ctx, hashOf(token), key.ID, s.ttl); err != nil {
		return "", fmt.Errorf("session: start: %w", err)
	}
	return token, nil
}

// Key answers the key a token's session is of; store.ErrNotFound for no
// session, one past its time, one older than a session now lasts, and one
// whose key was revoked.
func (s *Service) Key(ctx context.Context, token string) (store.Key, error) {
	if token == "" {
		return store.Key{}, store.ErrNotFound
	}
	k, _, err := s.store.SessionKey(ctx, hashOf(token), s.ttl)
	return k, err
}

// LookFor keeps the address a session's owner is looking for, "" for
// none. It ends with the session, however that ends.
func (s *Service) LookFor(ctx context.Context, token, address string) error {
	return s.store.LookFor(ctx, hashOf(token), address)
}

// LookingFor answers that address, "" for none.
func (s *Service) LookingFor(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	return s.store.LookingFor(ctx, hashOf(token))
}

// End ends a token's session.
func (s *Service) End(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, hashOf(token))
}
