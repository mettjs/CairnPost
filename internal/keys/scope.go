package keys

import (
	"context"
	"errors"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
)

// ErrDomain is an admin key bound to a sending domain reaching for a key
// of another domain, or of none.
var ErrDomain = errors.New("keys: this key manages only the keys of its own sending domain")

// ListFor is the keys an admin key may see, newest first, at most limit:
// every key for one bound to no domain, and for one bound to a domain
// only the keys bound to it.
func (s *Service) ListFor(ctx context.Context, scope string, limit int) ([]store.Key, error) {
	return s.store.ListKeys(ctx, scope, limit)
}

// MakeFor makes a key on behalf of an admin key. One bound to a domain
// makes only keys bound to that domain: it cannot make itself a way out.
func (s *Service) MakeFor(ctx context.Context, scope string, n Request) (string, store.Key, error) {
	n.Domain = strings.ToLower(strings.TrimSpace(n.Domain))
	switch {
	case scope != "" && n.Domain == "":
		n.Domain = scope
	case scope != "" && n.Domain != scope:
		return "", store.Key{}, ErrDomain
	}
	return s.Make(ctx, n)
}

// RevokeFor ends a key on behalf of an admin key; store.ErrNotFound for
// none such alive, or none the admin key may see.
func (s *Service) RevokeFor(ctx context.Context, scope, prefix string) error {
	if _, err := s.Find(ctx, scope, prefix); err != nil {
		return err
	}
	return s.store.RevokeKey(ctx, prefix)
}

// Find answers a key an admin key may see by its prefix; store.ErrNotFound
// otherwise.
func (s *Service) Find(ctx context.Context, scope, prefix string) (store.Key, error) {
	k, err := s.store.KeyByPrefix(ctx, prefix)
	if err != nil {
		return store.Key{}, err
	}
	if scope != "" && k.Domain != scope {
		return store.Key{}, store.ErrNotFound
	}
	return k, nil
}
