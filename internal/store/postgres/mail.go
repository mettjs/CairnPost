package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mettjs/cairnpost/internal/store"
)

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Message implements store.Mail.
func (db *DB) Message(ctx context.Context, id string) (store.Message, error) {
	if !uuidShape.MatchString(id) {
		return store.Message{}, store.ErrNotFound
	}
	m, err := email(ctx, db.pool, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Message{}, fmt.Errorf("postgres: read a message: %w", err)
	}
	return m, err
}

// messageColumns are a message's row, as scanMessage reads it.
const messageColumns = `m.id::text, m.email_id::text, m.api_key_id::text, m.sender, m.recipient, m.kind, m.driver,
	coalesce(m.upstream_id, ''), m.state, m.created_at, m.updated_at`

func scanMessage(row pgx.Row) (store.Message, error) {
	var m store.Message
	err := row.Scan(&m.ID, &m.EmailID, &m.KeyID, &m.Sender, &m.Recipient, &m.Kind, &m.Driver, &m.UpstreamID, &m.State, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// email reads an email by a well-formed id, with every address of it;
// store.ErrNotFound for none. The id is the email's: its first row's.
func email(ctx context.Context, q querier, id string) (store.Message, error) {
	m, err := scanMessage(q.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages m WHERE m.id = $1::uuid AND m.email_id = m.id`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Message{}, store.ErrNotFound
	}
	if err != nil {
		return store.Message{}, err
	}
	of, err := recipientsOf(ctx, q, []string{id})
	m.Recipients = of[id]
	return m, err
}

// recipientsOf answers every address of each of some emails, in the
// order they were given at the send.
func recipientsOf(ctx context.Context, q querier, emails []string) (map[string][]store.Recipient, error) {
	out := map[string][]store.Recipient{}
	if len(emails) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT email_id::text, recipient, kind, state, updated_at FROM messages
		WHERE email_id = ANY($1::uuid[]) ORDER BY email_id, id`, emails)
	if err != nil {
		return nil, err
	}
	var (
		of string
		r  store.Recipient
	)
	_, err = pgx.ForEachRow(rows, []any{&of, &r.Address, &r.Kind, &r.State, &r.UpdatedAt}, func() error {
		out[of] = append(out[of], r)
		return nil
	})
	return out, err
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
			WHERE email_id = $1::uuid`, id, upstreamID)
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
			UPDATE messages SET state = 'failed', updated_at = now() WHERE email_id = $1::uuid`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres: mark a message failed: %w", err)
	}
	return nil
}

// Lost implements store.Mail.
func (db *DB) Lost(ctx context.Context, id string) error {
	if err := markUnknown(ctx, db.pool, true, id); err != nil {
		return fmt.Errorf("postgres: mark a message's answer lost: %w", err)
	}
	return nil
}

// execer is what a pool and a transaction both do.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// markUnknown records that what became of messages is not known: each was
// handed to the upstream and its answer never came. An id may be an
// email's, for every address of it, or one address's row. It is the one place
// that says so, whoever found out: the send itself, the next request under
// its key, the pruner forgetting the key, or the sweep that finds it
// abandoned.
//
// happened says that something came of the email just now (a try that
// ended, a retry, its key forgotten), which its record is then kept
// from. A send found abandoned long after is not that: its record stays
// kept from when it was sent.
func markUnknown(ctx context.Context, q execer, happened bool, ids ...string) error {
	_, err := q.Exec(ctx, `
		UPDATE messages SET state = 'unknown', updated_at = CASE WHEN $2 THEN now() ELSE updated_at END
		WHERE (id = ANY($1::uuid[]) OR email_id = ANY($1::uuid[])) AND state = 'sending'`, ids, happened)
	return err
}

// insertMessage records an email as sending, a row for each address,
// and answers its id. It is the one way a send starts, so it is where a
// suppressed address is left out: the email goes to the others, and the
// addresses left out are answered. With none left it records nothing
// and answers "".
//
// The list is read here, when the send starts, and not again: a send
// started in the second or so before a bounce is recorded still goes.
func insertMessage(ctx context.Context, tx pgx.Tx, s store.Start) (string, []string, error) {
	domain := store.DomainOf(s.Sender)
	var addresses, kinds, folded []string
	for _, r := range s.Recipients {
		_, key := store.SuppressionKey(s.Sender, r.Address)
		addresses, kinds, folded = append(addresses, r.Address), append(kinds, r.Kind), append(folded, key)
	}
	rows, err := tx.Query(ctx, `
		SELECT w.n FROM unnest($2::text[]) WITH ORDINALITY AS w(address, n)
		WHERE EXISTS (SELECT 1 FROM suppressions s WHERE s.domain = $1 AND s.address = w.address AND s.lifted_at IS NULL)
		ORDER BY w.n`, domain, folded)
	if err != nil {
		return "", nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return "", nil, err
	}
	var dropped []string
	for i := len(out) - 1; i >= 0; i-- {
		at := out[i] - 1
		dropped = append([]string{addresses[at]}, dropped...)
		addresses, kinds = slices.Delete(addresses, at, at+1), slices.Delete(kinds, at, at+1)
	}
	if len(addresses) == 0 {
		return "", dropped, nil
	}
	// The email's id is its first row's: made first, so it is the oldest.
	var id string
	err = tx.QueryRow(ctx, `
		WITH email AS (SELECT uuidv7() AS id)
		INSERT INTO messages (id, email_id, api_key_id, sender, recipient, kind, driver, state, sender_domain)
		SELECT CASE WHEN w.n = 1 THEN email.id ELSE uuidv7() END, email.id, $1::uuid, $2, w.recipient, w.kind, $5, 'sending', $6
		FROM email, unnest($3::text[], $4::text[]) WITH ORDINALITY AS w(recipient, kind, n)
		RETURNING email_id::text`, s.KeyID, s.Sender, addresses, kinds, s.Driver, domain).Scan(&id)
	if err != nil || s.Content == nil {
		return id, dropped, err
	}
	// With its message, in the one step: a message is never without the
	// content it was to be kept with, and a send that is rolled back
	// leaves none behind.
	_, err = tx.Exec(ctx, `
		INSERT INTO message_contents (message_id, content, expires_at)
		VALUES ($1::uuid, $2, now() + make_interval(secs => $3))`, id, s.Content(), s.KeepContent.Seconds())
	return id, dropped, err
}

// Content implements store.Mail. Its age is read on the database's clock.
func (db *DB) Content(ctx context.Context, id string, keep time.Duration) ([]byte, time.Time, error) {
	if !uuidShape.MatchString(id) {
		return nil, time.Time{}, nil
	}
	var (
		content []byte
		until   time.Time
	)
	err := db.pool.QueryRow(ctx, `
		SELECT content, least(expires_at, created_at + make_interval(secs => $2)) FROM message_contents
		WHERE message_id = $1::uuid AND expires_at > now() AND created_at > now() - make_interval(secs => $2)`, id, keep.Seconds()).Scan(&content, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("postgres: read a message's content: %w", err)
	}
	return content, until, nil
}
