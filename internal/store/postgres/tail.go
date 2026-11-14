package postgres

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// EventsAfter implements store.Events. An event's id is made when it is
// recorded and ids are in time order, so "after" is "with a greater id".
func (db *DB) EventsAfter(ctx context.Context, after, domain string, limit int) ([]store.Event, error) {
	const columns = `e.id::text, m.email_id::text, e.driver, e.kind, e.permanent, coalesce(e.code, ''),
		coalesce(e.reason, ''), e.occurred_at, m.sender, m.recipient`
	query, args, newest := `
		SELECT `+columns+` FROM events e JOIN messages m ON m.id = e.message_id
		WHERE e.id > $1::uuid AND ($2 = '' OR m.sender_domain = $2) ORDER BY e.id LIMIT $3`, []any{after, domain, limit}, false
	switch {
	case after == "":
		// The last of them, read newest first and turned round.
		query, args, newest = `
			SELECT `+columns+` FROM events e JOIN messages m ON m.id = e.message_id
			WHERE ($1 = '' OR m.sender_domain = $1) ORDER BY e.id DESC LIMIT $2`, []any{domain, limit}, true
	case !uuidShape.MatchString(after):
		return nil, nil
	}
	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: read the events recorded: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Event, error) {
		var e store.Event
		err := row.Scan(&e.ID, &e.MessageID, &e.Driver, &e.Kind, &e.Permanent, &e.Code, &e.Reason, &e.At, &e.Sender, &e.Recipient)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: read the events recorded: %w", err)
	}
	if newest {
		slices.Reverse(out)
	}
	return out, nil
}
