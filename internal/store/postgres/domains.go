package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// SendingDomains implements store.Domains. The domains messages were
// sent from are read by hopping through the index from each name to the
// next, so the cost is one step a domain, not one a message, and the
// hopping stops at the limit: the first names of either kind hold the
// first names of both.
func (db *DB) SendingDomains(ctx context.Context, limit int) ([]store.SendingDomain, error) {
	rows, err := db.pool.Query(ctx, `
		WITH RECURSIVE sent(name, n) AS (
			(SELECT sender_domain, 1 FROM messages ORDER BY sender_domain LIMIT 1)
			UNION ALL
			SELECT (SELECT sender_domain FROM messages WHERE sender_domain > sent.name ORDER BY sender_domain LIMIT 1), n + 1
			FROM sent WHERE sent.name IS NOT NULL AND n < $1
		), bound AS (
			SELECT domain AS name, count(*) AS keys FROM api_keys
			WHERE domain IS NOT NULL AND revoked_at IS NULL GROUP BY domain ORDER BY domain LIMIT $1
		)
		SELECT n.name, coalesce(b.keys, 0),
		       (SELECT created_at FROM messages WHERE sender_domain = n.name ORDER BY id DESC LIMIT 1)
		FROM (SELECT name FROM sent WHERE name IS NOT NULL UNION SELECT name FROM bound) n
		LEFT JOIN bound b USING (name)
		ORDER BY n.name LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list sending domains: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.SendingDomain, error) {
		var d store.SendingDomain
		err := row.Scan(&d.Name, &d.Keys, &d.LastSent)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: list sending domains: %w", err)
	}
	return out, nil
}

// SendingDomain implements store.Domains.
func (db *DB) SendingDomain(ctx context.Context, name string) (store.SendingDomain, error) {
	d := store.SendingDomain{Name: name}
	err := db.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM api_keys WHERE domain = $1 AND revoked_at IS NULL),
		       (SELECT created_at FROM messages WHERE sender_domain = $1 ORDER BY id DESC LIMIT 1)`, name).Scan(&d.Keys, &d.LastSent)
	if err != nil {
		return store.SendingDomain{}, fmt.Errorf("postgres: read a sending domain: %w", err)
	}
	if d.Keys == 0 && d.LastSent == nil {
		return store.SendingDomain{}, store.ErrNotFound
	}
	return d, nil
}
