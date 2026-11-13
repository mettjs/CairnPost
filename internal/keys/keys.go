// Package keys makes, checks and revokes API keys. A key is shown once, when
// it is made; what is kept is its public prefix and a SHA-256 of the whole.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
)

// ErrInvalid is a key that is malformed, unknown or revoked. One error for
// all three: a caller is not told which.
var ErrInvalid = errors.New("keys: not a valid API key")

// lead starts every key, so one is recognisable in a log or a leak scan.
const lead = "cp_"

// Service is the key use cases.
type Service struct {
	store store.Keys
	// max is how many live keys one sending domain may have; 0 for any.
	max int
}

// New returns the service.
func New(s store.Keys) *Service { return &Service{store: s} }

// WithMax returns the service limited to max live keys for one sending
// domain, and as many bound to none.
func (s *Service) WithMax(max int) *Service { return &Service{store: s.store, max: max} }

// Invalid is a key that cannot be made as asked; Reason says what to fix.
type Invalid struct{ Reason string }

func (e *Invalid) Error() string { return "keys: " + e.Reason }

// ErrLimit is one key more than there may be: of one sending domain, or
// of the keys bound to none.
var ErrLimit = errors.New("keys: there are as many keys of this kind as CAIRNPOST_MAX_KEYS allows; revoke one that is no longer used")

// Request is a key to make.
type Request struct {
	// Name says what will use the key.
	Name string
	// Scope is store.ScopeSend or store.ScopeAdmin.
	Scope string
	// Domain is the one sending domain it may send from, "" for any.
	Domain string
	// By is the prefix of the key that asked for it, "" for none.
	By string
}

// Create makes a key and answers it in full, the only time it exists
// outside its holder's hands.
func (s *Service) Create(ctx context.Context, name, scope, domain string) (string, store.Key, error) {
	return s.Make(ctx, Request{Name: name, Scope: scope, Domain: domain})
}

// Make is Create with who asked. Its errors are an *Invalid, ErrLimit, or
// a failure of the store.
func (s *Service) Make(ctx context.Context, n Request) (string, store.Key, error) {
	name, domain, err := checked(n.Name, n.Domain)
	if err != nil {
		return "", store.Key{}, err
	}
	if n.Scope != store.ScopeSend && n.Scope != store.ScopeAdmin {
		return "", store.Key{}, &Invalid{fmt.Sprintf("the scope is %q or %q", store.ScopeSend, store.ScopeAdmin)}
	}
	pre, secret := make([]byte, 4), make([]byte, 32)
	if _, err := rand.Read(pre); err != nil {
		return "", store.Key{}, fmt.Errorf("keys: no randomness: %w", err)
	}
	if _, err := rand.Read(secret); err != nil {
		return "", store.Key{}, fmt.Errorf("keys: no randomness: %w", err)
	}
	prefix := lead + hex.EncodeToString(pre)
	full := prefix + "_" + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(full))
	k, err := s.store.CreateKey(ctx, store.Key{Name: name, Prefix: prefix, Hash: sum[:], Scope: n.Scope,
		Domain: domain, CreatedBy: n.By}, s.max)
	if errors.Is(err, store.ErrLimit) {
		return "", store.Key{}, ErrLimit
	}
	if err != nil {
		return "", store.Key{}, fmt.Errorf("keys: create: %w", err)
	}
	return full, k, nil
}

// Authenticate answers the key a presented string is, or ErrInvalid.
func (s *Service) Authenticate(ctx context.Context, presented string) (store.Key, error) {
	prefix, ok := prefixOf(presented)
	if !ok {
		return store.Key{}, ErrInvalid
	}
	k, err := s.store.KeyByPrefix(ctx, prefix)
	if errors.Is(err, store.ErrNotFound) {
		return store.Key{}, ErrInvalid
	}
	if err != nil {
		return store.Key{}, fmt.Errorf("keys: look up: %w", err)
	}
	sum := sha256.Sum256([]byte(presented))
	if subtle.ConstantTimeCompare(sum[:], k.Hash) != 1 || k.RevokedAt != nil {
		return store.Key{}, ErrInvalid
	}
	return k, nil
}

// List is every key, revoked ones included, newest first.
func (s *Service) List(ctx context.Context) ([]store.Key, error) {
	return s.store.ListKeys(ctx, "", 0)
}

// Revoke ends a key by its prefix; store.ErrNotFound for none such alive.
func (s *Service) Revoke(ctx context.Context, prefix string) error {
	return s.store.RevokeKey(ctx, prefix)
}

// prefixOf is the public start of a well-formed key: "cp_" and eight hex.
func prefixOf(key string) (string, bool) {
	if !strings.HasPrefix(key, lead) || len(key) < len(lead)+8+1+43 || key[len(lead)+8] != '_' {
		return "", false
	}
	prefix := key[:len(lead)+8]
	if _, err := hex.DecodeString(prefix[len(lead):]); err != nil {
		return "", false
	}
	return prefix, true
}
