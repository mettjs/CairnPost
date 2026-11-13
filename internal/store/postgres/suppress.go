package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// Suppress implements store.Suppressions. By hand, a lifted suppression
// comes back into force whatever its history.
func (db *DB) Suppress(ctx context.Context, s store.Suppression) (bool, error) {
	tag, err := db.pool.Exec(ctx, `
		INSERT INTO suppressions (domain, address, reason, message_id, note)
		VALUES ($1, $2, $3, nullif($4, '')::uuid, nullif($5, ''))
		ON CONFLICT (domain, address) DO UPDATE
		SET reason = EXCLUDED.reason, message_id = EXCLUDED.message_id, note = EXCLUDED.note,
		    created_at = now(), lifted_at = NULL
		WHERE suppressions.lifted_at IS NOT NULL`,
		store.Fold(s.Domain), store.Fold(s.Address), s.Reason, s.MessageID, s.Note)
	if err != nil {
		return false, fmt.Errorf("postgres: suppress an address: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// suppressByEvent puts a suppression in force because of an event about a
// message sent at sentAt. A suppression that was lifted comes back only for
// an email sent after the lift: an old email's late event does not undo it.
// One in force by an event that could name no message is given this one.
func suppressByEvent(ctx context.Context, tx pgx.Tx, domain, address, reason, messageID string, sentAt time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO suppressions (domain, address, reason, message_id)
		VALUES ($1, $2, $3, $4::uuid)
		ON CONFLICT (domain, address) DO UPDATE
		SET reason = EXCLUDED.reason, message_id = EXCLUDED.message_id, note = NULL,
		    created_at = now(), lifted_at = NULL
		WHERE suppressions.lifted_at IS NOT NULL AND suppressions.lifted_at < $5`,
		domain, address, reason, messageID, sentAt); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE suppressions SET message_id = $4::uuid
		WHERE domain = $1 AND address = $2 AND reason = $3 AND lifted_at IS NULL AND message_id IS NULL`,
		domain, address, reason, messageID)
	return err
}

// suppressUnrecorded puts a suppression in force because of an event about
// an email with no record here. When that email was sent is not known, so
// it never brings back a suppression that was lifted: if the address is
// dead, the next email to it says so.
func suppressUnrecorded(ctx context.Context, tx pgx.Tx, domain, address, reason string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO suppressions (domain, address, reason) VALUES ($1, $2, $3)
		ON CONFLICT (domain, address) DO NOTHING`, domain, address, reason)
	return err
}

// Unsuppress implements store.Suppressions.
func (db *DB) Unsuppress(ctx context.Context, domain, address string) error {
	tag, err := db.pool.Exec(ctx, `
		UPDATE suppressions SET lifted_at = now()
		WHERE domain = $1 AND address = $2 AND lifted_at IS NULL`, store.Fold(domain), store.Fold(address))
	if err != nil {
		return fmt.Errorf("postgres: lift a suppression: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// UnsuppressAll implements store.Suppressions. The rows are taken in the
// list's order, as every writer of many of them takes them.
func (db *DB) UnsuppressAll(ctx context.Context, domain, reason string) (int, error) {
	tag, err := db.pool.Exec(ctx, `
		UPDATE suppressions SET lifted_at = now() WHERE (domain, address) IN (
			SELECT domain, address FROM suppressions
			WHERE domain = $1 AND lifted_at IS NULL AND ($2 = '' OR reason = $2)
			ORDER BY domain, address FOR UPDATE)`, store.Fold(domain), reason)
	if err != nil {
		return 0, fmt.Errorf("postgres: lift a domain's suppressions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListSuppressions implements store.Suppressions.
func (db *DB) ListSuppressions(ctx context.Context, domain string, after *store.Suppression, limit int) ([]store.Suppression, error) {
	from := store.Suppression{}
	if after != nil {
		from = *after
	}
	rows, err := db.pool.Query(ctx, `
		SELECT `+suppressionColumns+`
		FROM suppressions
		WHERE lifted_at IS NULL AND ($1 = '' OR domain = $1) AND (domain, address) > ($2, $3)
		ORDER BY domain, address LIMIT $4`, store.Fold(domain), from.Domain, from.Address, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list suppressions: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanSuppression)
	if err != nil {
		return nil, fmt.Errorf("postgres: list suppressions: %w", err)
	}
	return out, nil
}

// SuppressionsOf implements store.Suppressions. An address has a row for
// each sending domain at most, so nothing limits the answer.
func (db *DB) SuppressionsOf(ctx context.Context, domain, address string) ([]store.Suppression, error) {
	sql, args := `SELECT `+suppressionColumns+` FROM suppressions WHERE lifted_at IS NULL AND address = $1`, []any{store.Fold(address)}
	if domain != "" {
		sql, args = sql+` AND domain = $2`, append(args, store.Fold(domain))
	}
	rows, err := db.pool.Query(ctx, sql+` ORDER BY domain`, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: find an address's suppressions: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanSuppression)
	if err != nil {
		return nil, fmt.Errorf("postgres: find an address's suppressions: %w", err)
	}
	return out, nil
}

const suppressionColumns = `domain, address, reason, coalesce(message_id::text, ''), coalesce(note, ''), created_at`

func scanSuppression(row pgx.CollectableRow) (store.Suppression, error) {
	var s store.Suppression
	err := row.Scan(&s.Domain, &s.Address, &s.Reason, &s.MessageID, &s.Note, &s.CreatedAt)
	return s, err
}
