package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// errLostRace is a claim another request made first: look again.
var errLostRace = errors.New("postgres: another request claimed the key first")

// Start implements store.Mail. Every comparison of time is the database's
// own clock against the database's own stamps.
func (db *DB) Start(ctx context.Context, s store.Start) (store.Outcome, string, error) {
	var (
		outcome store.Outcome
		id      string
	)
	// Losing the race for a new key means the winner's row is now there to
	// read; once more is always enough, three is for safety.
	for try := 0; try < 3; try++ {
		err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) (err error) {
			if s.IdemKey == "" {
				outcome = store.Started
				id, err = insertMessage(ctx, tx, s)
				return err
			}
			outcome, id, err = claim(ctx, tx, s)
			return err
		})
		if errors.Is(err, errLostRace) {
			continue
		}
		if err != nil {
			return 0, "", fmt.Errorf("postgres: start a send: %w", err)
		}
		return outcome, id, nil
	}
	return 0, "", errors.New("postgres: start a send: the idempotency key kept changing hands")
}

// claim decides what a request under an Idempotency-Key is, holding the
// key's row locked while it does.
func claim(ctx context.Context, tx pgx.Tx, s store.Start) (store.Outcome, string, error) {
	var (
		fingerprint    []byte
		messageID      string
		state          string
		expired, stale bool
	)
	err := tx.QueryRow(ctx, `
		SELECT fingerprint, message_id::text, state,
		       created_at <= now() - make_interval(secs => $3),
		       tried_at   <= now() - make_interval(secs => $4)
		FROM idempotency_keys WHERE api_key_id = $1::uuid AND key = $2 FOR UPDATE`,
		s.KeyID, s.IdemKey, s.TTL.Seconds(), s.InDoubt.Seconds()).Scan(
		&fingerprint, &messageID, &state, &expired, &stale)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return claimNew(ctx, tx, s)
	case err != nil:
		return 0, "", err
	case expired:
		// Remembered long enough: the key is new again.
		id, err := insertMessage(ctx, tx, s)
		if err != nil {
			return 0, "", err
		}
		_, err = tx.Exec(ctx, `
			UPDATE idempotency_keys
			SET fingerprint = $3, message_id = $4::uuid, state = 'in_flight', created_at = now(), tried_at = now()
			WHERE api_key_id = $1::uuid AND key = $2`, s.KeyID, s.IdemKey, s.Fingerprint, id)
		return store.Started, id, err
	case !bytes.Equal(fingerprint, s.Fingerprint):
		return store.Mismatch, "", nil
	case state == "done":
		return store.Replay, messageID, nil
	case !stale:
		return store.InFlight, "", nil
	}
	// The last try's answer never came: its message's fate is unknown, and
	// this request sends again.
	if _, err := tx.Exec(ctx, `
		UPDATE messages SET state = 'unknown', updated_at = now()
		WHERE id = $1::uuid AND state = 'sending'`, messageID); err != nil {
		return 0, "", err
	}
	id, err := insertMessage(ctx, tx, s)
	if err != nil {
		return 0, "", err
	}
	_, err = tx.Exec(ctx, `
		UPDATE idempotency_keys SET message_id = $3::uuid, tried_at = now()
		WHERE api_key_id = $1::uuid AND key = $2`, s.KeyID, s.IdemKey, id)
	return store.Started, id, err
}

// claimNew takes a key nobody holds. If another request takes it first, the
// insert waits for that request to commit and then inserts nothing.
func claimNew(ctx context.Context, tx pgx.Tx, s store.Start) (store.Outcome, string, error) {
	id, err := insertMessage(ctx, tx, s)
	if err != nil {
		return 0, "", err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (api_key_id, key, fingerprint, message_id, state)
		VALUES ($1::uuid, $2, $3, $4::uuid, 'in_flight')
		ON CONFLICT (api_key_id, key) DO NOTHING`, s.KeyID, s.IdemKey, s.Fingerprint, id)
	if err != nil {
		return 0, "", err
	}
	if tag.RowsAffected() == 0 {
		return 0, "", errLostRace // rolls back the message just inserted
	}
	return store.Started, id, nil
}
