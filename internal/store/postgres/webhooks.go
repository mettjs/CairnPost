package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// previousKeys is the keys an endpoint (aliased e) had before that
// webhooks are still signed with, the last changed first.
var previousKeys = `ARRAY(SELECT p.secret FROM webhook_previous_secrets p
		WHERE p.endpoint_id = e.id AND p.until > now() ORDER BY p.until DESC LIMIT ` + strconv.Itoa(store.PreviousKept) + `)`

// endpointColumns reads an endpoint aliased e.
var endpointColumns = `e.id::text, e.url, coalesce(e.domain, ''), e.kinds, coalesce(e.description, ''), e.secret, e.enabled, e.created_at,
	e.failing_since, e.paused_at, ` + previousKeys + `,
	(SELECT max(p.until) FROM webhook_previous_secrets p WHERE p.endpoint_id = e.id AND p.until > now())`

func scanEndpoint(row pgx.Row) (store.Endpoint, error) {
	var e store.Endpoint
	err := row.Scan(&e.ID, &e.URL, &e.Domain, &e.Kinds, &e.Description, &e.Secret, &e.Enabled, &e.CreatedAt,
		&e.FailingSince, &e.PausedAt, &e.Previous, &e.PreviousUntil)
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
			INSERT INTO webhook_endpoints AS e (url, domain, kinds, description, secret)
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
	e, err := scanEndpoint(db.pool.QueryRow(ctx, `SELECT `+endpointColumns+` FROM webhook_endpoints e WHERE e.id = $1::uuid`, id))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, fmt.Errorf("postgres: read a webhook endpoint: %w", err)
	}
	return e, err
}

// ListEndpoints implements store.Webhooks.
func (db *DB) ListEndpoints(ctx context.Context, domain string) ([]store.Endpoint, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT `+endpointColumns+` FROM webhook_endpoints e
		WHERE $1 = '' OR e.domain = $1 ORDER BY e.id`, store.Fold(domain))
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
		UPDATE webhook_endpoints e
		SET url = coalesce($2, e.url), kinds = coalesce($3, e.kinds),
		    description = CASE WHEN $4::text IS NULL THEN e.description ELSE nullif($4, '') END,
		    enabled = coalesce($5, e.enabled)
		WHERE e.id = $1::uuid RETURNING `+endpointColumns, id, c.URL, kinds, c.Description, c.Enabled))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, fmt.Errorf("postgres: change a webhook endpoint: %w", err)
	}
	return e, err
}

// lockEndpoint takes an endpoint's row for a change to how it stands,
// and answers whether there is one. Every such change takes it first, and
// only then the endpoint's deliveries: the order a removal takes them in,
// so the two never wait on each other in a circle. It does not hold up
// the recording of what the endpoint is owed.
func lockEndpoint(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var found bool
	err := tx.QueryRow(ctx, `SELECT true FROM webhook_endpoints WHERE id = $1::uuid FOR NO KEY UPDATE`, id).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return found, err
}

// RotateSecret implements store.Webhooks.
func (db *DB) RotateSecret(ctx context.Context, id string, secret []byte, overlap time.Duration) (store.Endpoint, error) {
	if !uuidShape.MatchString(id) {
		return store.Endpoint{}, store.ErrNotFound
	}
	var e store.Endpoint
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		found, err := lockEndpoint(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return store.ErrNotFound
		}
		if overlap > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO webhook_previous_secrets (endpoint_id, secret, until)
				SELECT id, secret, now() + make_interval(secs => $2) FROM webhook_endpoints WHERE id = $1::uuid`,
				id, overlap.Seconds()); err != nil {
				return err
			}
		}
		e, err = scanEndpoint(tx.QueryRow(ctx, `
			UPDATE webhook_endpoints e SET secret = $2 WHERE e.id = $1::uuid RETURNING `+endpointColumns, id, secret))
		return err
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, fmt.Errorf("postgres: change a webhook endpoint's signing key: %w", err)
	}
	return e, err
}

// ResumeEndpoint implements store.Webhooks. Whether it is paused is read
// with its row held, so a try that pauses it meanwhile comes before or
// after, never in between.
func (db *DB) ResumeEndpoint(ctx context.Context, id string) (store.Endpoint, bool, error) {
	if !uuidShape.MatchString(id) {
		return store.Endpoint{}, false, store.ErrNotFound
	}
	var (
		e       store.Endpoint
		resumed bool
	)
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		found, err := lockEndpoint(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return store.ErrNotFound
		}
		// Nobody was there to take what was given up while it failed: it
		// is owed again, one more try each.
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries d SET state = 'pending', next_at = now()
			FROM webhook_endpoints e
			WHERE e.id = $1::uuid AND d.endpoint_id = e.id AND `+givenUp, id); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT paused_at IS NOT NULL FROM webhook_endpoints WHERE id = $1::uuid`, id).Scan(&resumed); err != nil {
			return err
		}
		e, err = scanEndpoint(tx.QueryRow(ctx, `
			UPDATE webhook_endpoints e
			SET failing_since = CASE WHEN e.paused_at IS NULL THEN e.failing_since END, paused_at = NULL
			WHERE e.id = $1::uuid RETURNING `+endpointColumns, id))
		return err
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, false, fmt.Errorf("postgres: resume a webhook endpoint: %w", err)
	}
	return e, resumed, err
}

// EndpointAnswered implements store.Webhooks.
func (db *DB) EndpointAnswered(ctx context.Context, id string) error {
	if !uuidShape.MatchString(id) {
		return nil
	}
	_, err := db.pool.Exec(ctx, `
		UPDATE webhook_endpoints SET failing_since = NULL
		WHERE id = $1::uuid AND failing_since IS NOT NULL AND paused_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("postgres: record that a webhook endpoint answered: %w", err)
	}
	return nil
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
