package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PruneKeys implements store.Pruning. The key's row is taken before its
// message's, the order a takeover uses.
func (db *DB) PruneKeys(ctx context.Context, ttl time.Duration, limit int) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			DELETE FROM idempotency_keys WHERE (api_key_id, key) IN (
				SELECT api_key_id, key FROM idempotency_keys
				WHERE created_at <= now() - make_interval(secs => $1)
				ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)
			RETURNING message_id::text, state`, ttl.Seconds(), limit)
		if err != nil {
			return err
		}
		var inDoubt []string
		var id, state string
		if _, err := pgx.ForEachRow(rows, []any{&id, &state}, func() error {
			if n++; state == "in_flight" {
				inDoubt = append(inDoubt, id)
			}
			return nil
		}); err != nil || len(inDoubt) == 0 {
			return err
		}
		// A try that never got its answer: what became of its email is
		// not known, and with the key gone nothing else would say so.
		return markUnknown(ctx, tx, inDoubt...)
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: prune idempotency keys: %w", err)
	}
	return n, nil
}

// Horizon implements store.Pruning, on the database's clock: the stamps it
// is compared with are the database's own.
func (db *DB) Horizon(ctx context.Context, keep time.Duration) (time.Time, error) {
	var at time.Time
	if err := db.pool.QueryRow(ctx, `SELECT now() - make_interval(secs => $1)`, keep.Seconds()).Scan(&at); err != nil {
		return at, fmt.Errorf("postgres: read the time: %w", err)
	}
	return at, nil
}

// PruneMessages implements store.Pruning. Events and the deliveries owed
// for them go with a message by the schema's own rules.
//
// A message a key points at is passed over: a takeover holds the key and
// then wants the message, and a prune that held the message and then
// wanted the key would be its deadlock. Keys are pruned by their own rule.
func (db *DB) PruneMessages(ctx context.Context, before time.Time, limit int) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT m.id::text FROM messages m
			WHERE m.updated_at <= $1
			  AND NOT EXISTS (SELECT 1 FROM idempotency_keys k WHERE k.message_id = m.id)
			ORDER BY m.updated_at LIMIT $2 FOR UPDATE OF m SKIP LOCKED`, before, limit)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(ids) == 0 {
			return err
		}
		// The suppressions these messages caused let go of them. Their rows
		// are taken in the list's order, as lifting a whole domain takes
		// them, so the two never wait on each other in a circle.
		if _, err := tx.Exec(ctx, `
			SELECT 1 FROM suppressions WHERE message_id = ANY($1::uuid[])
			ORDER BY domain, address FOR UPDATE`, ids); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM messages WHERE id = ANY($1::uuid[])`, ids)
		n = int(tag.RowsAffected())
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: prune messages: %w", err)
	}
	return n, nil
}

// PruneLifted implements store.Pruning. What holds a lifted row back is a
// message made before its own lift, not before the cutoff: that set only
// shrinks, so every lifted row goes in the end, however busy the rest is.
func (db *DB) PruneLifted(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := db.pool.Exec(ctx, `
		DELETE FROM suppressions WHERE (domain, address) IN (
			SELECT s.domain, s.address FROM suppressions s
			WHERE s.lifted_at IS NOT NULL AND s.lifted_at <= $1
			  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.created_at <= s.lifted_at)
			ORDER BY s.lifted_at LIMIT $2 FOR UPDATE OF s SKIP LOCKED)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("postgres: prune lifted suppressions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
