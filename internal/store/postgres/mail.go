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
	coalesce(m.upstream_id, ''), m.state, m.created_at, m.updated_at, m.left_out`

func scanMessage(row pgx.Row) (store.Message, error) {
	var m store.Message
	err := row.Scan(&m.ID, &m.EmailID, &m.KeyID, &m.Sender, &m.Recipient, &m.Kind, &m.Driver, &m.UpstreamID, &m.State, &m.CreatedAt, &m.UpdatedAt, &m.LeftOut)
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
	// By the id as the database writes it, whatever case it was asked in.
	of, err := recipientsOf(ctx, q, []string{m.ID})
	m.Recipients = of[m.ID]
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

// Sent implements store.Mail. An address still failed was left out of
// the try that was taken (put on the suppression list between two tries
// of an email that waited): it stays as it is.
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
			WHERE email_id = $1::uuid AND state <> 'failed'`, id, upstreamID)
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

// Unsent implements store.Mail.
func (db *DB) Unsent(ctx context.Context, id string) error {
	if _, err := db.pool.Exec(ctx, `
		UPDATE messages SET state = 'failed', updated_at = now() WHERE email_id = $1::uuid`, id); err != nil {
		return fmt.Errorf("postgres: mark a message not sent this time: %w", err)
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
// and answers its id with who it goes to. It is the one way a send
// starts, so it is where a suppressed address is left out (store.Addressed):
// the email goes to the others, and the addresses left out are kept
// with it and answered. With nobody left it records nothing and
// answers "".
//
// The list is read here, when the send starts, and not again: a send
// started in the second or so before a bounce is recorded still goes.
func insertMessage(ctx context.Context, tx pgx.Tx, s store.Start) (string, []store.Addressee, []string, error) {
	kept, dropped, err := addressed(ctx, tx, s)
	if err != nil || len(kept) == 0 {
		return "", nil, dropped, err
	}
	domain := store.DomainOf(s.Sender)
	addresses, kinds := split(kept)
	// The email's id is its first row's: made first, so it is the oldest.
	var id string
	err = tx.QueryRow(ctx, `
		WITH email AS (SELECT coalesce(nullif($8, '')::uuid, uuidv7()) AS id)
		INSERT INTO messages (id, email_id, api_key_id, sender, recipient, kind, driver, state, sender_domain, left_out)
		SELECT CASE WHEN w.n = 1 THEN email.id ELSE uuidv7() END, email.id, $1::uuid, $2, w.recipient, w.kind, $5, 'sending', $6,
		       CASE WHEN w.n = 1 THEN $7::text[] ELSE '{}' END
		FROM email, unnest($3::text[], $4::text[]) WITH ORDINALITY AS w(recipient, kind, n)
		RETURNING email_id::text`, s.KeyID, s.Sender, addresses, kinds, s.Driver, domain, dropped, s.ID).Scan(&id)
	if err != nil || s.Content == nil {
		return id, kept, dropped, err
	}
	// With its message, in the one step: a message is never without the
	// content it was to be kept with, and a send that is rolled back
	// leaves none behind.
	_, err = tx.Exec(ctx, `
		INSERT INTO message_contents (message_id, content, expires_at)
		VALUES ($1::uuid, $2, now() + make_interval(secs => $3))`, id, s.Content(), s.KeepContent.Seconds())
	return id, kept, dropped, err
}

// addressed reads the suppression list for a send that starts now, and
// answers who the email goes to and who is left out (store.Addressed).
func addressed(ctx context.Context, tx pgx.Tx, s store.Start) (kept []store.Addressee, dropped []string, err error) {
	folded := make([]string, len(s.Recipients))
	for i, r := range s.Recipients {
		_, folded[i] = store.SuppressionKey(s.Sender, r.Address)
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT w.address FROM unnest($2::text[]) AS w(address)
		WHERE EXISTS (SELECT 1 FROM suppressions s WHERE s.domain = $1 AND s.address = w.address AND s.lifted_at IS NULL)`,
		store.DomainOf(s.Sender), folded)
	if err != nil {
		return nil, nil, err
	}
	listed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	kept, dropped = store.Addressed(s.Recipients, func(a store.Addressee) bool {
		_, key := store.SuppressionKey(s.Sender, a.Address)
		return slices.Contains(listed, key)
	})
	if dropped == nil {
		dropped = []string{}
	}
	return kept, dropped, nil
}

// split is some addressees as two lists, for unnest.
func split(of []store.Addressee) (addresses, kinds []string) {
	addresses, kinds = make([]string, len(of)), make([]string, len(of))
	for i, a := range of {
		addresses[i], kinds[i] = a.Address, a.Kind
	}
	return addresses, kinds
}

// resume starts an email under the id it waited with. The first time
// that is insertMessage. After a try that the provider did not take,
// the same rows are sending again, so the email is counted once: the
// suppression list is read anew, and an address put on it since stays
// failed and is named among those left out.
func resume(ctx context.Context, tx pgx.Tx, s store.Start) (string, []store.Addressee, []string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM messages WHERE email_id = $1::uuid ORDER BY id FOR UPDATE`, s.ID)
	if err != nil {
		return "", nil, nil, err
	}
	tried, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", nil, nil, err
	}
	if len(tried) == 0 {
		return insertMessage(ctx, tx, s)
	}
	kept, dropped, err := addressed(ctx, tx, s)
	if err != nil || len(kept) == 0 {
		return "", nil, dropped, err
	}
	addresses, kinds := split(kept)
	if _, err := tx.Exec(ctx, `
		UPDATE messages m SET state = 'sending', kind = w.kind, updated_at = now()
		FROM unnest($2::text[], $3::text[]) AS w(recipient, kind)
		WHERE m.email_id = $1::uuid AND m.recipient = w.recipient AND m.state = 'failed'`, s.ID, addresses, kinds); err != nil {
		return "", nil, nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE messages SET left_out = $2 WHERE id = $1::uuid`, s.ID, dropped)
	return s.ID, kept, dropped, err
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
