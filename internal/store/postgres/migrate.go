package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// migrateLock is the advisory lock two instances starting together contend
// for, so each migration runs once. The number is arbitrary and fixed.
const migrateLock = 7241903351

// Migrate applies every migration in files not yet applied, in name order,
// each in its own transaction, and answers the names it applied. A
// migration is a .sql file; its name up to the first "_" is its version.
func (db *DB) Migrate(ctx context.Context, files fs.FS) ([]string, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("postgres: list migrations: %w", err)
	}
	sort.Strings(names)
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(migrateLock)); err != nil {
		return nil, fmt.Errorf("postgres: migrate: take the lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(migrateLock))
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}
	var applied []string
	for _, name := range names {
		version, _, _ := strings.Cut(name, "_")
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`,
			version).Scan(&done); err != nil {
			return applied, fmt.Errorf("postgres: migrate: %w", err)
		}
		if done {
			continue
		}
		sql, err := fs.ReadFile(files, name)
		if err != nil {
			return applied, fmt.Errorf("postgres: read %s: %w", name, err)
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version)
			return err
		})
		if err != nil {
			return applied, fmt.Errorf("postgres: migration %s failed and was rolled back: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}
