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
	var m store.Message
	err := db.pool.QueryRow(ctx, `
		SELECT id::text, api_key_id::text, sender, recipient, driver, coalesce(upstream_id, ''),
		       state, created_at, updated_at
		FROM messages WHERE id = $1::uuid`, id).Scan(
		&m.ID, &m.KeyID, &m.Sender, &m.Recipient, &m.Driver, &m.UpstreamID, &m.State, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Message{}, store.ErrNotFound
	}
	if err != nil {
		return store.Message{}, fmt.Errorf("postgres: read a message: %w", err)
	}
	return m, nil
}

// Sent implements store.Mail.
func (db *DB) Sent(ctx context.Context, id, upstreamID string) error {
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE messages SET state = 'sent', upstream_id = $2, updated_at = now()
			WHERE id = $1::uuid`, id, upstreamID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE idempotency_keys SET state = 'done' WHERE message_id = $1::uuid`, id)
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

// insertMessage records a message as sending and answers its id.
func insertMessage(ctx context.Context, tx pgx.Tx, s store.Start) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO messages (api_key_id, sender, recipient, driver, state)
		VALUES ($1::uuid, $2, $3, $4, 'sending') RETURNING id::text`,
		s.KeyID, s.Sender, s.Recipient, s.Driver).Scan(&id)
	return id, err
}
