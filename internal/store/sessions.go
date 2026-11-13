package store

import (
	"context"
	"time"
)

// Sessions keeps who is signed in to the console. A session is named by
// the hash of its token; the token itself is never kept.
type Sessions interface {
	// CreateSession starts a session of a key that lasts ttl.
	CreateSession(ctx context.Context, hash []byte, keyID string, ttl time.Duration) error
	// SessionKey answers the key a session is of and when the session
	// ends. ErrNotFound for none, for one whose time is up, for one
	// started longer ago than ttl (so that making sessions shorter ends
	// the ones already begun), and for one whose key was revoked since.
	SessionKey(ctx context.Context, hash []byte, ttl time.Duration) (Key, time.Time, error)
	// DeleteSession ends a session; no error if there is none.
	DeleteSession(ctx context.Context, hash []byte) error
}
