// Package session is who is signed in to the console: it starts a session
// for a key, answers the key a session is of, and ends it. A session is a
// random token held by the browser; what is kept is its hash.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
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

// lookups is how many addresses a session keeps: the ones looked for,
// or shown, last. A page that names an older one says it is no longer
// kept, and shows the whole list.
const lookups = 20

// handleShape is what a handle LookFor made looks like.
var handleShape = regexp.MustCompile(`^[0-9a-f]{16}$`)

// LookFor keeps an address a session's owner looks for and answers the
// handle it is kept under, to name it by where the address itself must
// not be written: in a page's URL. "" for no address. It ends with the
// session, however that ends.
func (s *Service) LookFor(ctx context.Context, token, address string) (string, error) {
	if token == "" || address == "" {
		return "", nil
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("session: no randomness: %w", err)
	}
	return s.store.Remember(ctx, hashOf(token), hex.EncodeToString(raw), address, lookups)
}

// IsHandle reports whether a string could be a handle LookFor made.
func IsHandle(s string) bool { return handleShape.MatchString(s) }

// LookingFor answers the address a session keeps under a handle, "" for
// none: a handle of another session, one no longer kept, or no handle.
func (s *Service) LookingFor(ctx context.Context, token, handle string) (string, error) {
	if token == "" || !IsHandle(handle) {
		return "", nil
	}
	return s.store.Recall(ctx, hashOf(token), handle)
}

// Forget drops the address a session keeps under a handle: whoever
// looked for it asked to see everything again.
func (s *Service) Forget(ctx context.Context, token, handle string) error {
	if token == "" || !IsHandle(handle) {
		return nil
	}
	return s.store.Forget(ctx, hashOf(token), handle)
}

// End ends a token's session.
func (s *Service) End(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, hashOf(token))
}
