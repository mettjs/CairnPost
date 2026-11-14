package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// settling runs a settle with the delivery's endpoint held: the endpoint
// first and the delivery after, the order a removal takes them in. It
// answers the endpoint's id, "" when the delivery is gone.
func (db *DB) settling(ctx context.Context, id string, settle func(tx pgx.Tx, endpointID string) error) error {
	return pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		var endpointID string
		err := tx.QueryRow(ctx, `
			SELECT e.id::text FROM webhook_deliveries d JOIN webhook_endpoints e ON e.id = d.endpoint_id
			WHERE d.id = $1::uuid FOR NO KEY UPDATE OF e`, id).Scan(&endpointID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // removed meanwhile, with its endpoint or its email
		}
		if err != nil {
			return err
		}
		return settle(tx, endpointID)
	})
}

// DeliveryDone implements store.Webhooks.
func (db *DB) DeliveryDone(ctx context.Context, id string, status int) error {
	err := db.settling(ctx, id, func(tx pgx.Tx, endpointID string) error {
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries
			SET state = 'delivered', delivered_at = now(), last_status = $2, last_failure = NULL, taken_until = NULL
			WHERE id = $1::uuid AND state <> 'delivered'`, id, status)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		// It answered: it is no longer failing. One paused meanwhile
		// keeps when its failing began, for what resuming owes again.
		_, err = tx.Exec(ctx, `
			UPDATE webhook_endpoints SET failing_since = NULL
			WHERE id = $1::uuid AND failing_since IS NOT NULL AND paused_at IS NULL`, endpointID)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres: settle a webhook delivery: %w", err)
	}
	return nil
}

// DeliveryMissed implements store.Webhooks. The endpoint's row is
// written only when something about it changes: its first miss, and the
// miss that pauses it.
func (db *DB) DeliveryMissed(ctx context.Context, id string, attempt, status int, failure string, retryIn time.Duration, final bool,
	pauseAfter time.Duration) (bool, error) {
	var paused bool
	err := db.settling(ctx, id, func(tx pgx.Tx, endpointID string) error {
		var tried time.Time
		err := tx.QueryRow(ctx, `
			UPDATE webhook_deliveries
			SET state = CASE WHEN $6 THEN 'failed' ELSE 'pending' END, taken_until = NULL,
			    next_at = now() + make_interval(secs => $5), last_status = nullif($3, 0), last_failure = $4
			WHERE id = $1::uuid AND state = 'pending' AND attempts = $2
			RETURNING coalesce(attempted_at, now())`, id, attempt, status, failure, retryIn.Seconds(), final).Scan(&tried)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a try another has since overtaken settles nothing
		}
		if err != nil {
			return err
		}
		// The failing began when this try was made, if it is the first
		// to miss: what is given up from then on is what resuming owes
		// again, this one included. It is paused only by a webhook given
		// up, whose tries have then all failed, once the failing is old
		// enough.
		err = tx.QueryRow(ctx, `
			UPDATE webhook_endpoints e
			SET failing_since = coalesce(e.failing_since, $2),
			    paused_at = CASE WHEN $3 AND $4::float8 > 0
			                      AND coalesce(e.failing_since, $2) <= now() - make_interval(secs => $4) THEN now() END
			WHERE e.id = $1::uuid AND e.paused_at IS NULL
			  AND (e.failing_since IS NULL
			       OR ($3 AND $4::float8 > 0 AND e.failing_since <= now() - make_interval(secs => $4)))
			RETURNING e.paused_at IS NOT NULL`, endpointID, tried, final, pauseAfter.Seconds()).Scan(&paused)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // nothing about the endpoint changes
		}
		return err
	})
	if err != nil {
		return false, fmt.Errorf("postgres: settle a webhook delivery: %w", err)
	}
	return paused, nil
}

// DeliveryReleased implements store.Webhooks.
func (db *DB) DeliveryReleased(ctx context.Context, id string, attempt int) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE webhook_deliveries SET attempts = attempts - 1, taken_until = NULL
		WHERE id = $1::uuid AND state = 'pending' AND attempts = $2`, id, attempt)
	if err != nil {
		return fmt.Errorf("postgres: hand back a webhook delivery: %w", err)
	}
	return nil
}

// ListDeliveries implements store.Webhooks.
func (db *DB) ListDeliveries(ctx context.Context, endpointID, state, before string, limit int) ([]store.Delivery, error) {
	if !uuidShape.MatchString(endpointID) || (before != "" && !uuidShape.MatchString(before)) {
		return nil, nil
	}
	rows, err := db.pool.Query(ctx, `
		SELECT d.id::text, d.endpoint_id::text, coalesce(ev.kind, $5), coalesce(m.email_id::text, ''), d.state, d.attempts, d.next_at,
		       coalesce(d.last_status, 0), coalesce(d.last_failure, ''), d.created_at, d.attempted_at, d.delivered_at
		FROM webhook_deliveries d LEFT JOIN events ev ON ev.id = d.event_id LEFT JOIN messages m ON m.id = ev.message_id
		WHERE d.endpoint_id = $1::uuid AND ($2 = '' OR d.state = $2) AND ($3 = '' OR d.id < nullif($3, '')::uuid)
		ORDER BY d.id DESC LIMIT $4`, endpointID, state, before, limit, store.KindSuppressed)
	if err != nil {
		return nil, fmt.Errorf("postgres: list webhook deliveries: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Delivery, error) {
		var d store.Delivery
		err := row.Scan(&d.ID, &d.EndpointID, &d.Kind, &d.MessageID, &d.State, &d.Attempts, &d.NextAt,
			&d.LastStatus, &d.LastFailure, &d.CreatedAt, &d.AttemptedAt, &d.DeliveredAt)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: list webhook deliveries: %w", err)
	}
	return out, nil
}

// RetryDelivery implements store.Webhooks. A delivery being tried right
// now keeps its taker: it is not sent twice at once.
func (db *DB) RetryDelivery(ctx context.Context, endpointID, id string) error {
	if !uuidShape.MatchString(endpointID) || !uuidShape.MatchString(id) {
		return store.ErrNotFound
	}
	tag, err := db.pool.Exec(ctx, `
		UPDATE webhook_deliveries SET state = 'pending', next_at = now()
		WHERE id = $1::uuid AND endpoint_id = $2::uuid AND state IN ('failed', 'pending')`, id, endpointID)
	if err != nil {
		return fmt.Errorf("postgres: retry a webhook delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
