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
	// Remember keeps, with a session, an address its owner looks for,
	// and answers the handle it is kept under: the one given, or the one
	// that address already has in the session. A session keeps the keep
	// addresses looked for last, and the older ones go. It answers ""
	// and no error if there is no such session.
	Remember(ctx context.Context, hash []byte, handle, address string, keep int) (string, error)
	// Recall answers the address a session keeps under a handle, "" for
	// none and for no session. Being asked for counts as being looked
	// for: an address in use is not the one that goes.
	Recall(ctx context.Context, hash []byte, handle string) (string, error)
	// Forget drops the address a session keeps under a handle; no error
	// if there is none.
	Forget(ctx context.Context, hash []byte, handle string) error
	// DeleteSession ends a session; no error if there is none.
	DeleteSession(ctx context.Context, hash []byte) error
}
