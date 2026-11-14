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
		where = []string{`m.email_id = m.id`}
		args  []any
	)
	add := func(condition string, value any) string {
		args = append(args, value)
		return fmt.Sprintf(condition, len(args))
	}
	if q.Domain != "" {
		where = append(where, add(`m.sender_domain = $%d`, q.Domain))
	}
	// An address or a state is of one address of an email: the email is
	// listed if one of its addresses fits both.
	var one []string
	if q.Recipient != "" {
		one = append(one, add(`lower(r.recipient COLLATE "C") = lower($%d COLLATE "C")`, q.Recipient))
	}
	if q.State != "" {
		one = append(one, add(`r.state = $%d`, q.State))
	}
	if len(one) > 0 {
		where = append(where, `m.id IN (SELECT r.email_id FROM messages r WHERE `+strings.Join(one, ` AND `)+`)`)
	}
	if q.Before != "" {
		where = append(where, add(`m.id < $%d::uuid`, q.Before))
	}
	args = append(args, q.Limit)
	rows, err := db.pool.Query(ctx, `SELECT `+messageColumns+` FROM messages m WHERE `+strings.Join(where, ` AND `)+
		fmt.Sprintf(` ORDER BY m.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list messages: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Message, error) { return scanMessage(row) })
	if err != nil {
		return nil, fmt.Errorf("postgres: list messages: %w", err)
	}
	ids := make([]string, len(out))
	for i, m := range out {
		ids[i] = m.ID
	}
	of, err := recipientsOf(ctx, db.pool, ids)
	for i := range out {
		out[i].Recipients = of[out[i].ID]
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: list messages: %w", err)
	}
	return out, nil
}
