package postgres

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// EventsAfter implements store.Events. An event's id is made when it is
// recorded and ids are in time order, so "after" is "with a greater id".
func (db *DB) EventsAfter(ctx context.Context, after string, back time.Duration, domain string, limit int) ([]store.Event, error) {
	const columns = `e.id::text, m.email_id::text, e.driver, e.kind, e.permanent, coalesce(e.code, ''),
		coalesce(e.reason, ''), e.occurred_at, m.sender, m.recipient`
	query, args, newest := `
		SELECT `+columns+` FROM events e JOIN messages m ON m.id = e.message_id
		WHERE e.id > $1::uuid AND ($2 = '' OR m.sender_domain = $2) ORDER BY e.id LIMIT $3`, []any{earlier(after, back), domain, limit}, false
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

// earlier is the lowest id an event recorded by before id could have:
// an id begins with the millisecond it was made in. An id that is not
// of that form is answered as it is.
func earlier(id string, by time.Duration) string {
	if by <= 0 || !uuidShape.MatchString(id) {
		return id
	}
	ms, err := strconv.ParseInt(id[:8]+id[9:13], 16, 64)
	if err != nil {
		return id
	}
	ms = max(ms-by.Milliseconds(), 0)
	return fmt.Sprintf("%08x-%04x-7000-8000-000000000000", ms>>16, ms&0xffff)
}
