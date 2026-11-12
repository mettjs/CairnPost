// Package postgres is the store over PostgreSQL. It is the only package
// that issues SQL.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mettjs/cairnpost/internal/store"
)

// DB is the store.
type DB struct{ pool *pgxpool.Pool }

var _ store.Store = (*DB)(nil)

// Open connects to the database at dsn and checks that it answers.
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: the database does not answer: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close releases every connection.
func (db *DB) Close() { db.pool.Close() }

// Ping implements store.Store.
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }
