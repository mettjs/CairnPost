package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

const keyColumns = `id::text, name, prefix, hash, scope, coalesce(domain, ''), coalesce(created_by, ''), created_at, revoked_at`

func scanKey(row pgx.Row) (store.Key, error) {
	var k store.Key
	err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Hash, &k.Scope, &k.Domain, &k.CreatedBy, &k.CreatedAt, &k.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Key{}, store.ErrNotFound
	}
	return k, err
}

// CreateKey implements store.Keys. The count and the insert are one step
// under a lock, so two requests cannot both take the last place.
func (db *DB) CreateKey(ctx context.Context, k store.Key, max int) (store.Key, error) {
	var out store.Key
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) (err error) {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cairnpost.api_keys'))`); err != nil {
			return err
		}
		out, err = scanKey(tx.QueryRow(ctx, `
			INSERT INTO api_keys (name, prefix, hash, scope, domain, created_by)
			SELECT $1, $2, $3, $4, nullif($5, ''), nullif($6, '')
			WHERE $7 = 0 OR (SELECT count(*) FROM api_keys
				WHERE revoked_at IS NULL AND domain IS NOT DISTINCT FROM nullif($5, '')) < $7
			RETURNING `+keyColumns, k.Name, k.Prefix, k.Hash, k.Scope, k.Domain, k.CreatedBy, max))
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return store.Key{}, store.ErrLimit // nothing was inserted: the count was reached
	}
	if err != nil {
		return store.Key{}, fmt.Errorf("postgres: create a key: %w", err)
	}
	return out, nil
}

// KeyByPrefix implements store.Keys.
func (db *DB) KeyByPrefix(ctx context.Context, prefix string) (store.Key, error) {
	k, err := scanKey(db.pool.QueryRow(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE prefix = $1`, prefix))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Key{}, fmt.Errorf("postgres: find a key: %w", err)
	}
	return k, err
}

// ListKeys implements store.Keys.
func (db *DB) ListKeys(ctx context.Context, domain string, limit int) ([]store.Key, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+keyColumns+` FROM api_keys
		WHERE $1 = '' OR domain = $1 ORDER BY created_at DESC, id DESC LIMIT nullif($2, 0)`, domain, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list keys: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Key, error) { return scanKey(row) })
	if err != nil {
		return nil, fmt.Errorf("postgres: list keys: %w", err)
	}
	return out, nil
}

// RevokeKey implements store.Keys.
func (db *DB) RevokeKey(ctx context.Context, prefix string) error {
	// With it go the emails it asked for ahead of time: what they said
	// is not kept for a key nobody may use any more.
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE api_keys SET revoked_at = now() WHERE prefix = $1 AND revoked_at IS NULL`, prefix)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return cancelOf(ctx, tx, prefix)
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("postgres: revoke a key: %w", err)
	}
	return err
}
