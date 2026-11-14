package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// cancelled is what cancelling sets: the email will not be sent, and
// what it said is gone with its files.
const cancelled = `state = 'cancelled', words = NULL, bytes = 0, claimed_at = NULL, updated_at = now()`

// ending changes one email that is in a state, and deletes its files
// when told to. Its error for an email that is not in that state is
// ErrTooLate, and ErrNotFound for none.
func (db *DB) ending(ctx context.Context, what, id, state, set string, files bool, args ...any) error {
	if !uuidShape.MatchString(id) {
		return store.ErrNotFound
	}
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE scheduled_emails SET `+set+` WHERE id = $1::uuid AND state = $2`,
			append([]any{id, state}, args...)...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var there bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM scheduled_emails WHERE id = $1::uuid)`, id).Scan(&there); err != nil {
				return err
			}
			if there {
				return store.ErrTooLate
			}
			return store.ErrNotFound
		}
		if files {
			_, err = tx.Exec(ctx, `DELETE FROM scheduled_files WHERE email_id = $1::uuid`, id)
		}
		return err
	})
	if err != nil && !errors.Is(err, store.ErrTooLate) && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("postgres: %s a waiting email: %w", what, err)
	}
	return err
}

// Cancel implements store.Schedule.
func (db *DB) Cancel(ctx context.Context, id string) error {
	return db.ending(ctx, "cancel", id, "scheduled", cancelled+`, reason = ''`, true)
}

// Drop implements store.Schedule.
func (db *DB) Drop(ctx context.Context, id, reason string) error {
	return db.ending(ctx, "drop", id, "sending", cancelled+`, reason = $3`, true, reason)
}

// Reschedule implements store.Schedule.
func (db *DB) Reschedule(ctx context.Context, id string, at time.Time) error {
	return db.ending(ctx, "reschedule", id, "scheduled", `send_at = $3, next_try_at = $3, updated_at = now()`, false, at)
}

// Postpone implements store.Schedule.
func (db *DB) Postpone(ctx context.Context, id string, wait time.Duration) error {
	return db.ending(ctx, "postpone", id, "sending",
		`state = 'scheduled', claimed_at = NULL, next_try_at = now() + make_interval(secs => $3), updated_at = now()`, false, wait.Seconds())
}

// Release implements store.Schedule.
func (db *DB) Release(ctx context.Context, id string) error {
	if !uuidShape.MatchString(id) {
		return nil
	}
	if _, err := db.pool.Exec(ctx, `DELETE FROM scheduled_emails WHERE id = $1::uuid AND state = 'sending'`, id); err != nil {
		return fmt.Errorf("postgres: release a waiting email: %w", err)
	}
	return nil
}

// TakeDue implements store.Schedule. Whether an email's time has come
// is asked of the database's clock.
func (db *DB) TakeDue(ctx context.Context, limit int) ([]store.Held, error) {
	var out []store.Held
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH due AS (
				SELECT s.id, k.revoked_at IS NOT NULL AS revoked FROM scheduled_emails s JOIN api_keys k ON k.id = s.api_key_id
				WHERE s.state = 'scheduled' AND s.next_try_at <= now()
				ORDER BY s.next_try_at LIMIT $1 FOR UPDATE OF s SKIP LOCKED)
			UPDATE scheduled_emails s SET
				state = CASE WHEN due.revoked THEN 'cancelled' ELSE 'sending' END,
				reason = CASE WHEN due.revoked THEN $2 ELSE s.reason END,
				words = CASE WHEN due.revoked THEN NULL ELSE s.words END,
				bytes = CASE WHEN due.revoked THEN 0 ELSE s.bytes END,
				claimed_at = CASE WHEN due.revoked THEN NULL ELSE now() END,
				tries = s.tries + CASE WHEN due.revoked THEN 0 ELSE 1 END, updated_at = now()
			FROM due WHERE s.id = due.id RETURNING `+heldColumns, limit, store.HeldKeyRevoked)
		if err != nil {
			return err
		}
		taken, err := pgx.CollectRows(rows, heldRow)
		if err != nil {
			return err
		}
		var gone []string
		for _, h := range taken {
			if h.State == store.StateCancelled {
				gone = append(gone, h.ID)
			} else {
				out = append(out, h)
			}
		}
		if len(gone) > 0 {
			_, err = tx.Exec(ctx, `DELETE FROM scheduled_files WHERE email_id = ANY($1::uuid[])`, gone)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: take the emails whose time has come: %w", err)
	}
	return out, nil
}

// Recover implements store.Schedule.
func (db *DB) Recover(ctx context.Context, older time.Duration, limit int) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT s.id::text, EXISTS (SELECT 1 FROM messages m WHERE m.email_id = s.id AND m.state <> 'failed')
			FROM scheduled_emails s WHERE s.state = 'sending' AND s.claimed_at <= now() - make_interval(secs => $1)
			ORDER BY s.claimed_at LIMIT $2 FOR UPDATE OF s SKIP LOCKED`, older.Seconds(), limit)
		if err != nil {
			return err
		}
		var handed, not []string
		var id string
		var went bool
		if _, err := pgx.ForEachRow(rows, []any{&id, &went}, func() error {
			if n++; went {
				handed = append(handed, id)
			} else {
				not = append(not, id)
			}
			return nil
		}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM scheduled_emails WHERE id = ANY($1::uuid[])`, handed); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE scheduled_emails SET state = 'scheduled', claimed_at = NULL, next_try_at = now(), updated_at = now()
			WHERE id = ANY($1::uuid[])`, not)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: recover the emails a stopped server had taken: %w", err)
	}
	return n, nil
}

// PruneCancelled implements store.Pruning.
func (db *DB) PruneCancelled(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := db.pool.Exec(ctx, `
		DELETE FROM scheduled_emails WHERE id IN (
			SELECT id FROM scheduled_emails WHERE state = 'cancelled' AND updated_at <= $1
			ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("postgres: prune the cancelled emails: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
