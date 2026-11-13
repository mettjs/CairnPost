package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Message implements store.Mail.
func (db *DB) Message(ctx context.Context, id string) (store.Message, error) {
	if !uuidShape.MatchString(id) {
		return store.Message{}, store.ErrNotFound
	}
	m, err := message(ctx, db.pool, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Message{}, fmt.Errorf("postgres: read a message: %w", err)
	}
	return m, err
}

// message reads one message by a well-formed id; store.ErrNotFound for none.
func message(ctx context.Context, q querier, id string) (store.Message, error) {
	var m store.Message
	err := q.QueryRow(ctx, `
		SELECT id::text, api_key_id::text, sender, recipient, driver, coalesce(upstream_id, ''),
		       state, created_at, updated_at
		FROM messages WHERE id = $1::uuid`, id).Scan(
		&m.ID, &m.KeyID, &m.Sender, &m.Recipient, &m.Driver, &m.UpstreamID, &m.State, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Message{}, store.ErrNotFound
	}
	return m, err
}

// Sent implements store.Mail.
func (db *DB) Sent(ctx context.Context, id, upstreamID string) error {
	// The key's row is touched before the message's, here as in Start, so
	// two transactions never wait on each other in opposite orders.
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE idempotency_keys SET state = 'done' WHERE message_id = $1::uuid`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE messages SET state = 'sent', upstream_id = nullif($2, ''), updated_at = now()
			WHERE id = $1::uuid`, id, upstreamID)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres: mark a message sent: %w", err)
	}
	return nil
}

// Failed implements store.Mail.
func (db *DB) Failed(ctx context.Context, id string) error {
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		// The key is freed first: its row points at the message.
		if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE message_id = $1::uuid`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE messages SET state = 'failed', updated_at = now() WHERE id = $1::uuid`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres: mark a message failed: %w", err)
	}
	return nil
}

// Lost implements store.Mail.
func (db *DB) Lost(ctx context.Context, id string) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE messages SET state = 'unknown', updated_at = now()
		WHERE id = $1::uuid AND state = 'sending'`, id)
	if err != nil {
		return fmt.Errorf("postgres: mark a message's answer lost: %w", err)
	}
	return nil
}

// insertMessage records a message as sending and answers its id. It is the
// one way a send starts, so it is where a suppressed recipient is refused:
// it then records nothing and answers "". One statement does both.
//
// The list is read here, when the send starts, and not again: a send
// started in the second or so before a bounce is recorded still goes.
func insertMessage(ctx context.Context, tx pgx.Tx, s store.Start) (string, error) {
	domain, address := store.SuppressionKey(s.Sender, s.Recipient)
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO messages (api_key_id, sender, recipient, driver, state)
		SELECT $1::uuid, $2, $3, $4, 'sending'
		WHERE NOT EXISTS (SELECT 1 FROM suppressions WHERE domain = $5 AND address = $6 AND lifted_at IS NULL)
		RETURNING id::text`,
		s.KeyID, s.Sender, s.Recipient, s.Driver, domain, address).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
