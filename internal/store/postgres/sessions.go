package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// CreateSession implements store.Sessions.
func (db *DB) CreateSession(ctx context.Context, hash []byte, keyID string, ttl time.Duration) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO console_sessions (hash, api_key_id, expires_at)
		VALUES ($1, $2::uuid, now() + make_interval(secs => $3))`, hash, keyID, ttl.Seconds())
	if err != nil {
		return fmt.Errorf("postgres: start a console session: %w", err)
	}
	return nil
}

// SessionKey implements store.Sessions. Whether its time is up is asked of
// the database's clock, which stamped it.
func (db *DB) SessionKey(ctx context.Context, hash []byte, ttl time.Duration) (store.Key, time.Time, error) {
	var (
		k     store.Key
		until time.Time
	)
	err := db.pool.QueryRow(ctx, `
		SELECT k.id::text, k.name, k.prefix, k.hash, k.scope, coalesce(k.domain, ''), coalesce(k.created_by, ''),
		       k.created_at, k.revoked_at, least(s.expires_at, s.created_at + make_interval(secs => $2))
		FROM console_sessions s JOIN api_keys k ON k.id = s.api_key_id
		WHERE s.hash = $1 AND s.expires_at > now() AND s.created_at > now() - make_interval(secs => $2)
		  AND k.revoked_at IS NULL`, hash, ttl.Seconds()).Scan(
		&k.ID, &k.Name, &k.Prefix, &k.Hash, &k.Scope, &k.Domain, &k.CreatedBy, &k.CreatedAt, &k.RevokedAt, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Key{}, time.Time{}, store.ErrNotFound
	}
	if err != nil {
		return store.Key{}, time.Time{}, fmt.Errorf("postgres: read a console session: %w", err)
	}
	return k, until, nil
}

// DeleteSession implements store.Sessions.
func (db *DB) DeleteSession(ctx context.Context, hash []byte) error {
	if _, err := db.pool.Exec(ctx, `DELETE FROM console_sessions WHERE hash = $1`, hash); err != nil {
		return fmt.Errorf("postgres: end a console session: %w", err)
	}
	return nil
}

// PruneSessions implements store.Pruning.
func (db *DB) PruneSessions(ctx context.Context, limit int) (int, error) {
	tag, err := db.pool.Exec(ctx, `
		DELETE FROM console_sessions WHERE hash IN (
			SELECT hash FROM console_sessions WHERE expires_at <= now()
			ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED)`, limit)
	if err != nil {
		return 0, fmt.Errorf("postgres: prune console sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
