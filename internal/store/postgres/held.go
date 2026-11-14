package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// heldColumns are a waiting email's, as heldRow reads them. An email
// taken for a try still waits, as far as anyone asking is told.
const heldColumns = `s.id::text, s.api_key_id::text, s.sender, s.recipients, s.send_at,
	CASE WHEN s.state = 'cancelled' THEN 'cancelled' ELSE 'scheduled' END, s.reason, s.tries, s.created_at, s.updated_at`

// addressee is an address as it is kept with a waiting email.
type addressee struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
}

func heldRow(row pgx.CollectableRow) (store.Held, error) {
	var (
		h   store.Held
		raw []byte
		to  []addressee
	)
	if err := row.Scan(&h.ID, &h.KeyID, &h.Sender, &raw, &h.SendAt, &h.State, &h.Reason, &h.Tries, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return h, err
	}
	if err := json.Unmarshal(raw, &to); err != nil {
		return h, errors.New("postgres: a waiting email's addresses are not what was written")
	}
	for _, a := range to {
		h.Recipients = append(h.Recipients, store.Addressee{Address: a.Address, Kind: a.Kind})
	}
	return h, nil
}

// hold keeps an email to be sent later and answers the id it waits
// under, "" when the emails that wait hold as much as they may.
func hold(ctx context.Context, tx pgx.Tx, s store.Start) (string, error) {
	size := int64(len(s.Hold.Words))
	to := make([]addressee, len(s.Recipients))
	for i, r := range s.Recipients {
		to[i] = addressee{r.Address, r.Kind}
	}
	for _, f := range s.Hold.Files {
		size += int64(len(f.Content))
	}
	if s.Hold.Room > 0 {
		var held int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(bytes), 0)::bigint FROM scheduled_emails`).Scan(&held); err != nil {
			return "", err
		}
		if held+size > s.Hold.Room {
			return "", nil
		}
	}
	recipients, _ := json.Marshal(to) // strings always encode
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO scheduled_emails (api_key_id, sender, sender_domain, recipients, send_at, state, next_try_at, words, bytes)
		VALUES ($1::uuid, $2, $3, $4, $5, 'scheduled', $5, $6, $7) RETURNING id::text`,
		s.KeyID, s.Sender, store.DomainOf(s.Sender), recipients, s.Hold.SendAt, s.Hold.Words, size).Scan(&id)
	for i, f := range s.Hold.Files {
		if err != nil {
			break
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO scheduled_files (email_id, position, filename, content_type, content_id, content)
			VALUES ($1::uuid, $2, $3, $4, $5, $6)`, id, i, f.Filename, f.Type, f.ContentID, f.Content)
	}
	return id, err
}

// Held implements store.Schedule.
func (db *DB) Held(ctx context.Context, id string) (store.Held, error) {
	if !uuidShape.MatchString(id) {
		return store.Held{}, store.ErrNotFound
	}
	rows, err := db.pool.Query(ctx, `SELECT `+heldColumns+` FROM scheduled_emails s WHERE s.id = $1::uuid`, id)
	if err != nil {
		return store.Held{}, fmt.Errorf("postgres: read a waiting email: %w", err)
	}
	h, err := pgx.CollectExactlyOneRow(rows, heldRow)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Held{}, store.ErrNotFound
	}
	if err != nil {
		return store.Held{}, fmt.Errorf("postgres: read a waiting email: %w", err)
	}
	return h, nil
}

// HeldWords implements store.Schedule.
func (db *DB) HeldWords(ctx context.Context, id string) ([]byte, []store.HeldFile, error) {
	if !uuidShape.MatchString(id) {
		return nil, nil, nil
	}
	var words []byte
	err := db.pool.QueryRow(ctx, `SELECT words FROM scheduled_emails WHERE id = $1::uuid`, id).Scan(&words)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && words == nil {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: read what a waiting email says: %w", err)
	}
	rows, err := db.pool.Query(ctx, `
		SELECT filename, content_type, content_id, content FROM scheduled_files WHERE email_id = $1::uuid ORDER BY position`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: read a waiting email's files: %w", err)
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (f store.HeldFile, err error) {
		return f, row.Scan(&f.Filename, &f.Type, &f.ContentID, &f.Content)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: read a waiting email's files: %w", err)
	}
	return words, files, nil
}

// ListHeld implements store.Schedule.
func (db *DB) ListHeld(ctx context.Context, domain string, after time.Time, limit int) ([]store.Held, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT `+heldColumns+` FROM scheduled_emails s
		WHERE s.state <> 'cancelled' AND ($1 = '' OR s.sender_domain = $1) AND s.send_at > $2
		ORDER BY s.send_at, s.id LIMIT $3`, domain, after, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list the waiting emails: %w", err)
	}
	out, err := pgx.CollectRows(rows, heldRow)
	if err != nil {
		return nil, fmt.Errorf("postgres: list the waiting emails: %w", err)
	}
	return out, nil
}

// Waiting implements store.Schedule.
func (db *DB) Waiting(ctx context.Context, domain string) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx, `
		SELECT count(*) FROM scheduled_emails WHERE state <> 'cancelled' AND ($1 = '' OR sender_domain = $1)`, domain).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("postgres: count the waiting emails: %w", err)
	}
	return n, nil
}
