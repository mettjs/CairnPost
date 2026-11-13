package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// oweWebhooks writes, for an event just recorded, what each endpoint that
// hears about it is owed. It runs in the event's own transaction.
func oweWebhooks(ctx context.Context, tx pgx.Tx, eventID, domain, kind string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO webhook_deliveries (endpoint_id, event_id)
		SELECT id, $1::uuid FROM webhook_endpoints
		WHERE enabled AND (domain IS NULL OR domain = $2) AND (kinds = '{}' OR $3 = ANY (kinds))`,
		eventID, domain, kind)
	return err
}

// TakeDeliveries implements store.Webhooks. The rows are locked only while
// they are taken: taken_until, on the database's clock, is what keeps
// another sender off them while they are tried.
func (db *DB) TakeDeliveries(ctx context.Context, max, perEndpoint int, busy []string, lease time.Duration) ([]store.Due, error) {
	if busy == nil {
		busy = []string{}
	}
	rows, err := db.pool.Query(ctx, `
		WITH picked AS (
			SELECT d.id FROM webhook_endpoints e
			CROSS JOIN LATERAL (
				SELECT id, next_at FROM webhook_deliveries
				WHERE endpoint_id = e.id AND state = 'pending' AND next_at <= now()
				  AND (taken_until IS NULL OR taken_until <= now())
				ORDER BY next_at LIMIT $2 FOR UPDATE SKIP LOCKED) d
			WHERE e.enabled AND e.id::text <> ALL ($4::text[])
			ORDER BY d.next_at LIMIT $1
		), taken AS (
			UPDATE webhook_deliveries d
			SET attempts = d.attempts + 1, attempted_at = now(), taken_until = now() + make_interval(secs => $3)
			FROM picked WHERE d.id = picked.id
			RETURNING d.id, d.endpoint_id, d.event_id, d.attempts, d.created_at
		)
		SELECT t.id::text, t.attempts, t.created_at,
		       e.id::text, e.url, e.secret,
		       ev.id::text, ev.kind, ev.permanent, coalesce(ev.code, ''), coalesce(ev.reason, ''), ev.occurred_at, ev.suppressed,
		       m.id::text, m.sender, m.recipient, m.created_at
		FROM taken t
		JOIN webhook_endpoints e ON e.id = t.endpoint_id
		JOIN events ev ON ev.id = t.event_id
		JOIN messages m ON m.id = ev.message_id
		ORDER BY t.id`, max, perEndpoint, lease.Seconds(), busy)
	if err != nil {
		return nil, fmt.Errorf("postgres: take webhook deliveries: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Due, error) {
		var d store.Due
		err := row.Scan(&d.Delivery.ID, &d.Delivery.Attempts, &d.Delivery.CreatedAt,
			&d.Endpoint.ID, &d.Endpoint.URL, &d.Endpoint.Secret,
			&d.Event.ID, &d.Event.Kind, &d.Event.Permanent, &d.Event.Code, &d.Event.Reason, &d.Event.At, &d.Event.Suppressed,
			&d.Message.ID, &d.Message.Sender, &d.Message.Recipient, &d.Message.CreatedAt)
		d.Delivery.EndpointID, d.Delivery.Kind, d.Delivery.MessageID = d.Endpoint.ID, d.Event.Kind, d.Message.ID
		d.Event.MessageID = d.Message.ID
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: take webhook deliveries: %w", err)
	}
	return out, nil
}
