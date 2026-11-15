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

// What ending does once an email is changed.
const (
	nothing = iota
	// forget deletes what it said beside its own row.
	forget
	// unsend marks the addresses of a try under way as not sent to.
	unsend
)

// taken is the condition an email is one taking's under: $2 is the
// claim.
const taken = `state = 'sending' AND claim = $2`

// ending changes one email that fits a condition, and then does what
// after says. Its error for an email that does not fit is ErrTooLate, and
// ErrNotFound for none. $1 is the id; args are $2 on.
func (db *DB) ending(ctx context.Context, what, id, where, set string, after int, args ...any) error {
	if !uuidShape.MatchString(id) {
		return store.ErrNotFound
	}
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE scheduled_emails SET `+set+` WHERE id = $1::uuid AND `+where, append([]any{id}, args...)...)
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
		switch after {
		case forget:
			return forgetWords(ctx, tx, []string{id})
		case unsend:
			// With it, in the one step: an email put back to wait has
			// no address left sending, or it could never be begun again.
			_, err = tx.Exec(ctx, `
				UPDATE messages SET state = 'failed', updated_at = now() WHERE email_id = $1::uuid AND state = 'sending'`, id)
		}
		return err
	})
	if err != nil && !errors.Is(err, store.ErrTooLate) && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("postgres: %s a waiting email: %w", what, err)
	}
	return err
}

// forgetWords deletes what cancelled emails said beside their own rows:
// their files, and what a failed try of them kept of their words.
func forgetWords(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scheduled_files WHERE email_id = ANY($1::uuid[])`, ids); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM message_contents WHERE message_id = ANY($1::uuid[])`, ids)
	return err
}

// Cancel implements store.Schedule.
func (db *DB) Cancel(ctx context.Context, id string) error {
	return db.ending(ctx, "cancel", id, `state = 'scheduled'`, cancelled+`, reason = ''`, forget)
}

// Drop implements store.Schedule.
func (db *DB) Drop(ctx context.Context, id string, claim int, reason string) error {
	return db.ending(ctx, "drop", id, taken, cancelled+`, reason = $3`, forget, claim, reason)
}

// Reschedule implements store.Schedule. A new time is a new start: the
// tries made for the old one are not held against it.
func (db *DB) Reschedule(ctx context.Context, id string, at time.Time) error {
	return db.ending(ctx, "reschedule", id, `state = 'scheduled'`, `send_at = $2, next_try_at = $2, tries = 0, updated_at = now()`, nothing, at)
}

// Postpone implements store.Schedule.
func (db *DB) Postpone(ctx context.Context, id string, claim int, wait time.Duration) error {
	return db.ending(ctx, "postpone", id, taken,
		`state = 'scheduled', claimed_at = NULL, next_try_at = now() + make_interval(secs => $3), updated_at = now()`, unsend, claim, wait.Seconds())
}

// Release implements store.Schedule.
func (db *DB) Release(ctx context.Context, id string, claim int) error {
	if !uuidShape.MatchString(id) {
		return nil
	}
	if _, err := db.pool.Exec(ctx, `DELETE FROM scheduled_emails WHERE id = $1::uuid AND `+taken, id, claim); err != nil {
		return fmt.Errorf("postgres: release a waiting email: %w", err)
	}
	return nil
}

// settled ends the wait of an email whose message was just settled for
// good, in the same step: so a server that stops right after leaves
// nothing that could be taken for an email still to send.
func settled(ctx context.Context, q execer, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM scheduled_emails WHERE id = $1::uuid AND state = 'sending'`, id)
	return err
}

// begun marks a taken email as begun under its taking: false when it
// is no longer that taker's.
func begun(ctx context.Context, tx pgx.Tx, id string, claim int) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE scheduled_emails SET claimed_at = now(), tries = tries + 1 WHERE id = $1::uuid AND `+taken, id, claim)
	return tag.RowsAffected() == 1, err
}

// cancelOf cancels the waiting emails of an API key that was revoked.
func cancelOf(ctx context.Context, tx pgx.Tx, prefix string) error {
	rows, err := tx.Query(ctx, `
		UPDATE scheduled_emails s SET `+cancelled+`, reason = $2
		FROM api_keys k WHERE k.id = s.api_key_id AND k.prefix = $1 AND s.state = 'scheduled' RETURNING s.id::text`,
		prefix, store.HeldKeyRevoked)
	if err != nil {
		return err
	}
	gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	return forgetWords(ctx, tx, gone)
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
				claim = s.claim + CASE WHEN due.revoked THEN 0 ELSE 1 END, updated_at = now()
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
		return forgetWords(ctx, tx, gone)
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: take the emails whose time has come: %w", err)
	}
	return out, nil
}

// Recover implements store.Schedule. An email whose message is settled
// for good is no longer here (settled), so of those left: one with a
// message not failed was being sent when its server stopped; any other
// was not begun, or its last try was not taken.
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
