package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// RecordEvent implements store.Events.
func (db *DB) RecordEvent(ctx context.Context, e store.Event) (store.Recorded, error) {
	outcome := store.Kept
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		// The message's row is locked while its state is decided, so two
		// events about one email are applied one after the other.
		var (
			messageID, state  string
			emailID           string
			sender, recipient string
			sentAt            time.Time
			newer             bool
		)
		reason, suppresses := store.SuppressionFor(e.Kind, e.Permanent, e.Code)
		// An email to several addresses has a row for each, all under the
		// upstream's one id: the event is about the address it names. One
		// that names an address the email was not sent to is about no
		// row, and so is one that names none, unless the email went to
		// one address: there is no telling whose it is, and it must not
		// suppress the wrong one.
		err := tx.QueryRow(ctx, `
			SELECT m.id::text, m.email_id::text, m.state, m.sender, m.recipient, m.created_at,
			       NOT EXISTS (SELECT 1 FROM events e WHERE e.message_id = m.id AND e.kind = m.state AND e.occurred_at >= $3)
			FROM messages m WHERE m.driver = $1 AND m.upstream_id = $2
			  AND (lower(m.recipient COLLATE "C") = lower($4 COLLATE "C")
			       OR NOT EXISTS (SELECT 1 FROM messages o WHERE o.email_id = m.email_id AND o.id <> m.id))
			ORDER BY (lower(m.recipient COLLATE "C") = lower($4 COLLATE "C")) DESC, m.id LIMIT 1 FOR UPDATE OF m`,
			e.Driver, e.UpstreamID, e.At, e.Recipient).Scan(&messageID, &emailID, &state, &sender, &recipient, &sentAt, &newer)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = store.NoMessage
			// The email has no record here: its send's answer was lost, or
			// another sender on the domain made it. An address that is dead
			// for the domain is dead all the same, if the event names it.
			if suppresses && e.Sender != "" && e.Recipient != "" {
				domain, address := store.SuppressionKey(e.Sender, e.Recipient)
				return suppressUnrecorded(ctx, tx, domain, address, reason)
			}
			return nil
		}
		if err != nil {
			return err
		}
		var eventID string
		err = tx.QueryRow(ctx, `
			INSERT INTO events (message_id, driver, upstream_event_id, kind, permanent, code, reason, occurred_at)
			VALUES ($1::uuid, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), $8)
			ON CONFLICT (driver, upstream_event_id) DO NOTHING RETURNING id::text`,
			messageID, e.Driver, e.UpstreamEventID, e.Kind, e.Permanent, e.Code, e.Reason, e.At).Scan(&eventID)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = store.Duplicate
			return nil
		}
		if err != nil {
			return err
		}
		if e.Detail != "" {
			// Kept until its email's content is, counted from the send;
			// a sentence that comes after that is not kept.
			if _, err := tx.Exec(ctx, `
				INSERT INTO event_details (event_id, detail, expires_at)
				SELECT $1::uuid, $2, $3::timestamptz + make_interval(secs => $4)
				WHERE $3::timestamptz + make_interval(secs => $4) > now()`,
				eventID, []byte(e.Detail), sentAt, e.KeepDetail.Seconds()); err != nil {
				return err
			}
		}
		// A new event is owed to the endpoints that hear about it, whether
		// or not it moves the message's state.
		if err := oweWebhooks(ctx, tx, eventID, store.DomainOf(sender), e.Kind); err != nil {
			return err
		}
		// A bounce for good, or a complaint, suppresses the address for the
		// domain that sent, in the same step as the event that says so.
		if suppresses {
			// The suppression names the email, not the row of one address.
			domain, address := store.SuppressionKey(sender, recipient)
			if err := suppressByEvent(ctx, tx, domain, address, reason, emailID, sentAt); err != nil {
				return err
			}
			// Whether the address is on the list now is kept with the
			// event: a suppression that was lifted is not brought back by
			// an older email, and the webhook must not say it was.
			if _, err := tx.Exec(ctx, `
				UPDATE events SET suppressed = true WHERE id = $1::uuid AND EXISTS (
					SELECT 1 FROM suppressions WHERE domain = $2 AND address = $3 AND lifted_at IS NULL)`,
				eventID, domain, address); err != nil {
				return err
			}
		}
		// Every event kept is something that happened to the email, and
		// its record is kept from then, whether or not its state moves.
		to, moves := state, store.Moves(state, e.Kind, newer)
		if moves {
			to = store.StateOf[e.Kind]
		}
		if _, err = tx.Exec(ctx, `UPDATE messages SET state = $2, updated_at = now() WHERE id = $1::uuid`, messageID, to); err != nil || !moves || to != store.StateFailed {
			return err
		}
		// An email that went to nobody is free to be tried again under
		// its key, as after a refusal at send. One that failed at some
		// addresses is not: it would go twice to the others.
		_, err = tx.Exec(ctx, `
			DELETE FROM idempotency_keys WHERE message_id = $1::uuid
			  AND NOT EXISTS (SELECT 1 FROM messages WHERE email_id = $1::uuid AND state <> 'failed')`, emailID)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: record an event: %w", err)
	}
	return outcome, nil
}

// EventsOf implements store.Events.
func (db *DB) EventsOf(ctx context.Context, messageID string) ([]store.Event, error) {
	if !uuidShape.MatchString(messageID) {
		return nil, nil
	}
	events, err := eventsOf(ctx, db.pool, messageID)
	if err != nil {
		return nil, fmt.Errorf("postgres: read a message's events: %w", err)
	}
	return events, nil
}

// Record implements store.Events: both reads see one snapshot, so the state
// answered never disagrees with the events answered beside it.
func (db *DB) Record(ctx context.Context, messageID string) (store.Message, []store.Event, error) {
	if !uuidShape.MatchString(messageID) {
		return store.Message{}, nil, store.ErrNotFound
	}
	var (
		m      store.Message
		events []store.Event
	)
	err := pgx.BeginTxFunc(ctx, db.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) (err error) {
			if m, err = email(ctx, tx, messageID); err != nil {
				return err
			}
			events, err = eventsOf(ctx, tx, messageID)
			return err
		})
	if errors.Is(err, store.ErrNotFound) {
		return store.Message{}, nil, err
	}
	if err != nil {
		return store.Message{}, nil, fmt.Errorf("postgres: read a message's record: %w", err)
	}
	return m, events, nil
}

// Details implements store.Events. Age is read on the database's clock.
func (db *DB) Details(ctx context.Context, messageID string, keep time.Duration) (map[string]string, error) {
	out := map[string]string{}
	if !uuidShape.MatchString(messageID) {
		return out, nil
	}
	rows, err := db.pool.Query(ctx, `
		SELECT d.event_id::text, d.detail FROM event_details d
		JOIN events e ON e.id = d.event_id JOIN messages m ON m.id = e.message_id
		WHERE m.email_id = $1::uuid AND d.expires_at > now()
		  AND m.created_at > now() - make_interval(secs => $2)`, messageID, keep.Seconds())
	if err != nil {
		return nil, fmt.Errorf("postgres: read a message's event details: %w", err)
	}
	var (
		id     string
		detail []byte
	)
	if _, err := pgx.ForEachRow(rows, []any{&id, &detail}, func() error { out[id] = string(detail); return nil }); err != nil {
		return nil, fmt.Errorf("postgres: read a message's event details: %w", err)
	}
	return out, nil
}

// querier is what a pool and a transaction both do.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func eventsOf(ctx context.Context, q querier, messageID string) ([]store.Event, error) {
	rows, err := q.Query(ctx, `
		SELECT e.id::text, e.message_id::text, e.driver, e.upstream_event_id, e.kind, e.permanent,
		       coalesce(e.code, ''), coalesce(e.reason, ''), e.occurred_at, m.recipient
		FROM events e JOIN messages m ON m.id = e.message_id
		WHERE m.email_id = $1::uuid ORDER BY e.occurred_at, e.id`, messageID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Event, error) {
		var e store.Event
		err := row.Scan(&e.ID, &e.MessageID, &e.Driver, &e.UpstreamEventID, &e.Kind, &e.Permanent,
			&e.Code, &e.Reason, &e.At, &e.Recipient)
		return e, err
	})
}
