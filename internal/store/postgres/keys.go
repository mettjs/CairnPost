package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

const keyColumns = `id::text, name, prefix, hash, scope, coalesce(domain, ''), created_at, revoked_at`

func scanKey(row pgx.Row) (store.Key, error) {
	var k store.Key
	err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Hash, &k.Scope, &k.Domain, &k.CreatedAt, &k.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Key{}, store.ErrNotFound
	}
	return k, err
}

// CreateKey implements store.Keys.
func (db *DB) CreateKey(ctx context.Context, k store.Key) (store.Key, error) {
	out, err := scanKey(db.pool.QueryRow(ctx, `
		INSERT INTO api_keys (name, prefix, hash, scope, domain)
		VALUES ($1, $2, $3, $4, nullif($5, ''))
		RETURNING `+keyColumns, k.Name, k.Prefix, k.Hash, k.Scope, k.Domain))
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
func (db *DB) ListKeys(ctx context.Context) ([]store.Key, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+keyColumns+` FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list keys: %w", err)
	}
	defer rows.Close()
	var out []store.Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: list keys: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey implements store.Keys.
func (db *DB) RevokeKey(ctx context.Context, prefix string) error {
	tag, err := db.pool.Exec(ctx,
		`UPDATE api_keys SET revoked_at = now() WHERE prefix = $1 AND revoked_at IS NULL`, prefix)
	if err != nil {
		return fmt.Errorf("postgres: revoke a key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
