package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// Messages implements store.Mail. The statement holds only the conditions
// asked for, so each kind of list is planned for the index it reads: one
// statement with every condition made optional would be planned once for
// all of them, and read the whole table for the rarest.
func (db *DB) Messages(ctx context.Context, q store.MessageQuery) ([]store.Message, error) {
	if q.Before != "" && !uuidShape.MatchString(q.Before) {
		return nil, nil
	}
	var (
		where []string
		args  []any
	)
	add := func(condition string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if q.Domain != "" {
		add(`sender_domain = $%d`, q.Domain)
	}
	if q.Recipient != "" {
		add(`lower(recipient COLLATE "C") = lower($%d COLLATE "C")`, q.Recipient)
	}
	if q.State != "" {
		add(`state = $%d`, q.State)
	}
	if q.Before != "" {
		add(`id < $%d::uuid`, q.Before)
	}
	sql := `SELECT id::text, api_key_id::text, sender, recipient, driver, coalesce(upstream_id, ''),
		state, created_at, updated_at FROM messages`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	args = append(args, q.Limit)
	rows, err := db.pool.Query(ctx, sql+fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list messages: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Message, error) {
		var m store.Message
		err := row.Scan(&m.ID, &m.KeyID, &m.Sender, &m.Recipient, &m.Driver, &m.UpstreamID, &m.State, &m.CreatedAt, &m.UpdatedAt)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: list messages: %w", err)
	}
	return out, nil
}
