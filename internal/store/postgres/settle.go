package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// DeliveryDone implements store.Webhooks.
func (db *DB) DeliveryDone(ctx context.Context, id string, status int) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET state = 'delivered', delivered_at = now(), last_status = $2, last_failure = NULL, taken_until = NULL
		WHERE id = $1::uuid AND state <> 'delivered'`, id, status)
	if err != nil {
		return fmt.Errorf("postgres: settle a webhook delivery: %w", err)
	}
	return nil
}

// DeliveryMissed implements store.Webhooks.
func (db *DB) DeliveryMissed(ctx context.Context, id string, attempt, status int, failure string, retryIn time.Duration, final bool) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET state = CASE WHEN $6 THEN 'failed' ELSE 'pending' END, taken_until = NULL,
		    next_at = now() + make_interval(secs => $5), last_status = nullif($3, 0), last_failure = $4
		WHERE id = $1::uuid AND state = 'pending' AND attempts = $2`, id, attempt, status, failure, retryIn.Seconds(), final)
	if err != nil {
		return fmt.Errorf("postgres: settle a webhook delivery: %w", err)
	}
	return nil
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
		SELECT d.id::text, d.endpoint_id::text, ev.kind, ev.message_id::text, d.state, d.attempts, d.next_at,
		       coalesce(d.last_status, 0), coalesce(d.last_failure, ''), d.created_at, d.attempted_at, d.delivered_at
		FROM webhook_deliveries d JOIN events ev ON ev.id = d.event_id
		WHERE d.endpoint_id = $1::uuid AND ($2 = '' OR d.state = $2) AND ($3 = '' OR d.id < nullif($3, '')::uuid)
		ORDER BY d.id DESC LIMIT $4`, endpointID, state, before, limit)
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
