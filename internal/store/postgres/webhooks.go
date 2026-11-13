package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

const endpointColumns = `id::text, url, coalesce(domain, ''), kinds, coalesce(description, ''), secret, enabled, created_at`

func scanEndpoint(row pgx.Row) (store.Endpoint, error) {
	var e store.Endpoint
	err := row.Scan(&e.ID, &e.URL, &e.Domain, &e.Kinds, &e.Description, &e.Secret, &e.Enabled, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Endpoint{}, store.ErrNotFound
	}
	return e, err
}

// kindsOf is a list of kinds as the column keeps it: never NULL.
func kindsOf(kinds []string) []string {
	if kinds == nil {
		return []string{}
	}
	return kinds
}

// CreateEndpoint implements store.Webhooks. Makers wait for one another,
// so two at once cannot both be the last one allowed.
func (db *DB) CreateEndpoint(ctx context.Context, e store.Endpoint, max int) (store.Endpoint, error) {
	var out store.Endpoint
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) (err error) {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cairnpost.webhook_endpoints'))`); err != nil {
			return err
		}
		out, err = scanEndpoint(tx.QueryRow(ctx, `
			INSERT INTO webhook_endpoints (url, domain, kinds, description, secret)
			SELECT $1, nullif($2, ''), $3, nullif($4, ''), $5
			WHERE (SELECT count(*) FROM webhook_endpoints WHERE domain IS NOT DISTINCT FROM nullif($2, '')) < $6
			RETURNING `+endpointColumns, e.URL, store.Fold(e.Domain), kindsOf(e.Kinds), e.Description, e.Secret, max))
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, store.ErrLimit // nothing was inserted: the count was reached
	}
	if err != nil {
		return store.Endpoint{}, fmt.Errorf("postgres: create a webhook endpoint: %w", err)
	}
	return out, nil
}

// Endpoint implements store.Webhooks.
func (db *DB) Endpoint(ctx context.Context, id string) (store.Endpoint, error) {
	if !uuidShape.MatchString(id) {
		return store.Endpoint{}, store.ErrNotFound
	}
	e, err := scanEndpoint(db.pool.QueryRow(ctx, `SELECT `+endpointColumns+` FROM webhook_endpoints WHERE id = $1::uuid`, id))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, fmt.Errorf("postgres: read a webhook endpoint: %w", err)
	}
	return e, err
}

// ListEndpoints implements store.Webhooks.
func (db *DB) ListEndpoints(ctx context.Context, domain string) ([]store.Endpoint, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT `+endpointColumns+` FROM webhook_endpoints
		WHERE $1 = '' OR domain = $1 ORDER BY id`, store.Fold(domain))
	if err != nil {
		return nil, fmt.Errorf("postgres: list webhook endpoints: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Endpoint, error) { return scanEndpoint(row) })
	if err != nil {
		return nil, fmt.Errorf("postgres: list webhook endpoints: %w", err)
	}
	return out, nil
}

// UpdateEndpoint implements store.Webhooks.
func (db *DB) UpdateEndpoint(ctx context.Context, id string, c store.EndpointChange) (store.Endpoint, error) {
	if !uuidShape.MatchString(id) {
		return store.Endpoint{}, store.ErrNotFound
	}
	var kinds []string
	if c.Kinds != nil {
		kinds = kindsOf(*c.Kinds)
	}
	e, err := scanEndpoint(db.pool.QueryRow(ctx, `
		UPDATE webhook_endpoints
		SET url = coalesce($2, url), kinds = coalesce($3, kinds),
		    description = CASE WHEN $4::text IS NULL THEN description ELSE nullif($4, '') END,
		    enabled = coalesce($5, enabled)
		WHERE id = $1::uuid RETURNING `+endpointColumns, id, c.URL, kinds, c.Description, c.Enabled))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, fmt.Errorf("postgres: change a webhook endpoint: %w", err)
	}
	return e, err
}

// DeleteEndpoint implements store.Webhooks.
func (db *DB) DeleteEndpoint(ctx context.Context, id string) error {
	if !uuidShape.MatchString(id) {
		return store.ErrNotFound
	}
	tag, err := db.pool.Exec(ctx, `DELETE FROM webhook_endpoints WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete a webhook endpoint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
