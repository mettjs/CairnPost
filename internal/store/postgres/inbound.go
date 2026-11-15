package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

const inboundColumns = `id::text, domain, recipient, sender, subject, from_header, files, partial, bytes, received_at, expires_at`

func scanInbound(row pgx.Row) (store.Inbound, error) {
	var in store.Inbound
	err := row.Scan(&in.ID, &in.Domain, &in.Recipient, &in.Sender, &in.Subject, &in.From, &in.Files, &in.Partial, &in.Bytes,
		&in.ReceivedAt, &in.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Inbound{}, store.ErrNotFound
	}
	return in, err
}

// Receive implements store.Inbounds. One at a time: the room is read
// and the email written as one, and of the same email handed on twice
// at once the second finds the first.
func (db *DB) Receive(ctx context.Context, n store.NewInbound) (id string, again bool, err error) {
	err = pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cairnpost.inbound_emails'))`); err != nil {
			return err
		}
		// One whose time is up is no longer there to be the same as.
		if _, err := tx.Exec(ctx, `DELETE FROM inbound_emails WHERE recipient = $1 AND digest = $2 AND expires_at <= now()`,
			n.Recipient, n.Digest); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT id::text FROM inbound_emails WHERE recipient = $1 AND digest = $2`, n.Recipient, n.Digest).Scan(&id)
		if err == nil {
			again = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO inbound_emails (domain, recipient, sender, digest, raw, bytes, subject, from_header, files, partial, expires_at)
			SELECT $1, $2, $3, $4, $5, $6::integer, $7, $8, $9, $10, now() + make_interval(secs => $11)
			WHERE $12::bigint = 0 OR (SELECT coalesce(sum(bytes), 0) FROM inbound_emails WHERE expires_at > now()) + $6::integer <= $12::bigint
			RETURNING id::text`, n.Domain, n.Recipient, n.Sender, n.Digest, n.Raw, len(n.Raw), n.Subject, n.From, n.Files, n.Partial,
			n.Keep.Seconds(), n.Room).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrLimit
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO webhook_deliveries (endpoint_id, inbound_id)
			SELECT e.id, $1::uuid FROM webhook_endpoints e
			WHERE e.enabled AND (e.domain IS NULL OR e.domain = $2) AND (e.kinds = '{}' OR $3 = ANY (e.kinds))`,
			id, n.Domain, store.KindReceived)
		return err
	})
	if err != nil && !errors.Is(err, store.ErrLimit) {
		return "", false, fmt.Errorf("postgres: keep an email that came: %w", err)
	}
	return id, again, err
}

// Inbound implements store.Inbounds.
func (db *DB) Inbound(ctx context.Context, id string) (store.Inbound, error) {
	if !uuidShape.MatchString(id) {
		return store.Inbound{}, store.ErrNotFound
	}
	in, err := scanInbound(db.pool.QueryRow(ctx, `SELECT `+inboundColumns+` FROM inbound_emails
		WHERE id = $1::uuid AND expires_at > now()`, id))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Inbound{}, fmt.Errorf("postgres: find an email that came: %w", err)
	}
	return in, err
}

// InboundRaw implements store.Inbounds.
func (db *DB) InboundRaw(ctx context.Context, id string) ([]byte, error) {
	if !uuidShape.MatchString(id) {
		return nil, store.ErrNotFound
	}
	var raw []byte
	err := db.pool.QueryRow(ctx, `SELECT raw FROM inbound_emails WHERE id = $1::uuid AND expires_at > now()`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: read an email that came: %w", err)
	}
	return raw, nil
}

// ListInbound implements store.Inbounds.
func (db *DB) ListInbound(ctx context.Context, domain, before string, limit int) ([]store.Inbound, error) {
	if before != "" && !uuidShape.MatchString(before) {
		return nil, nil
	}
	rows, err := db.pool.Query(ctx, `SELECT `+inboundColumns+` FROM inbound_emails
		WHERE ($1 = '' OR domain = $1) AND ($2 = '' OR id < nullif($2, '')::uuid) AND expires_at > now()
		ORDER BY id DESC LIMIT $3`, domain, before, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list the emails that came: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Inbound, error) { return scanInbound(row) })
	if err != nil {
		return nil, fmt.Errorf("postgres: list the emails that came: %w", err)
	}
	return out, nil
}

// DeleteInbound implements store.Inbounds.
func (db *DB) DeleteInbound(ctx context.Context, id string) error {
	if !uuidShape.MatchString(id) {
		return store.ErrNotFound
	}
	tag, err := db.pool.Exec(ctx, `DELETE FROM inbound_emails WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete an email that came: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// CountInbound implements store.Inbounds.
func (db *DB) CountInbound(ctx context.Context, domain string) (n int, err error) {
	err = db.pool.QueryRow(ctx, `SELECT count(*) FROM inbound_emails WHERE ($1 = '' OR domain = $1) AND expires_at > now()`, domain).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("postgres: count the emails that came: %w", err)
	}
	return n, nil
}

// PruneInbound implements store.Pruning.
func (db *DB) PruneInbound(ctx context.Context, limit int) (int, error) {
	tag, err := db.pool.Exec(ctx, `
		DELETE FROM inbound_emails WHERE id IN (
			SELECT id FROM inbound_emails WHERE expires_at <= now()
			ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED)`, limit)
	if err != nil {
		return 0, fmt.Errorf("postgres: prune the emails that came: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
