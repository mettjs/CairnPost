package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// errLostRace is a claim another request made first: look again.
var errLostRace = errors.New("postgres: another request claimed the key first")

// Start implements store.Mail. Every comparison of time is the database's
// own clock against the database's own stamps.
func (db *DB) Start(ctx context.Context, s store.Start) (store.Claim, error) {
	var c store.Claim
	// Losing the race for a new key means the winner's row is now there to
	// read; once more is always enough, three is for safety.
	for try := 0; try < 3; try++ {
		err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) (err error) {
			if s.IdemKey == "" {
				c = store.Claim{Outcome: store.Started}
				if c.ID, c.Dropped, err = insertMessage(ctx, tx, s); c.ID == "" {
					c = store.Claim{Outcome: store.Suppressed}
				}
				return err
			}
			c, err = claim(ctx, tx, s)
			return err
		})
		if errors.Is(err, errLostRace) {
			continue
		}
		if err != nil {
			return store.Claim{}, fmt.Errorf("postgres: start a send: %w", err)
		}
		return c, nil
	}
	return store.Claim{}, errors.New("postgres: start a send: the idempotency key kept changing hands")
}

// claim decides what a request under an Idempotency-Key is, holding the
// key's row locked while it does.
func claim(ctx context.Context, tx pgx.Tx, s store.Start) (store.Claim, error) {
	var (
		fingerprint    []byte
		messageID      string
		state          string
		expired, stale bool
		wait           float64
	)
	err := tx.QueryRow(ctx, `
		SELECT fingerprint, message_id::text, state,
		       created_at <= now() - make_interval(secs => $3),
		       tried_at   <= now() - make_interval(secs => $4),
		       extract(epoch FROM tried_at + make_interval(secs => $4) - now())::float8
		FROM idempotency_keys WHERE api_key_id = $1::uuid AND key = $2 FOR UPDATE`,
		s.KeyID, s.IdemKey, s.TTL.Seconds(), s.InDoubt.Seconds()).Scan(
		&fingerprint, &messageID, &state, &expired, &stale, &wait)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return claimNew(ctx, tx, s)
	case err != nil:
		return store.Claim{}, err
	case expired, state == "in_flight" && stale:
		// Remembered long enough, or its last try's answer never came:
		// this request sends, whatever it is, and the key's life starts
		// again from now.
		return takeOver(ctx, tx, s, messageID, state == "in_flight")
	case state == "done" && !bytes.Equal(fingerprint, s.Fingerprint):
		return store.Claim{Outcome: store.Mismatch}, nil
	case state == "done":
		return store.Claim{Outcome: store.Replay, ID: messageID}, nil
	}
	return store.Claim{Outcome: store.InFlight, RetryAfter: time.Duration(wait * float64(time.Second))}, nil
}

// takeOver gives a key to a new send. The message it pointed at, if its
// answer never came, is recorded as unknown.
//
// If every recipient has been suppressed since, nothing is sent and the key
// is let go. When the earlier try was left in doubt, its message's id is
// answered with the refusal: its email may have gone, and its caller
// should be able to ask.
func takeOver(ctx context.Context, tx pgx.Tx, s store.Start, old string, inDoubt bool) (store.Claim, error) {
	if err := markUnknown(ctx, tx, true, old); err != nil {
		return store.Claim{}, err
	}
	id, dropped, err := insertMessage(ctx, tx, s)
	if err != nil {
		return store.Claim{}, err
	}
	if id == "" {
		_, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE api_key_id = $1::uuid AND key = $2`, s.KeyID, s.IdemKey)
		c := store.Claim{Outcome: store.Suppressed}
		if inDoubt {
			c.ID = old
		}
		return c, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE idempotency_keys
		SET fingerprint = $3, message_id = $4::uuid, state = 'in_flight', created_at = now(), tried_at = now()
		WHERE api_key_id = $1::uuid AND key = $2`, s.KeyID, s.IdemKey, s.Fingerprint, id)
	return store.Claim{Outcome: store.Started, ID: id, Dropped: dropped}, err
}

// claimNew takes a key nobody holds. If another request takes it first, the
// insert waits for that request to commit and then inserts nothing.
func claimNew(ctx context.Context, tx pgx.Tx, s store.Start) (store.Claim, error) {
	id, dropped, err := insertMessage(ctx, tx, s)
	if err != nil {
		return store.Claim{}, err
	}
	if id == "" {
		return store.Claim{Outcome: store.Suppressed}, nil
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (api_key_id, key, fingerprint, message_id, state)
		VALUES ($1::uuid, $2, $3, $4::uuid, 'in_flight')
		ON CONFLICT (api_key_id, key) DO NOTHING`, s.KeyID, s.IdemKey, s.Fingerprint, id)
	if err != nil {
		return store.Claim{}, err
	}
	if tag.RowsAffected() == 0 {
		return store.Claim{}, errLostRace // rolls back the message just inserted
	}
	return store.Claim{Outcome: store.Started, ID: id, Dropped: dropped}, nil
}
