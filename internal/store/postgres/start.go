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
				c, err = place(ctx, tx, s)
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

// place makes the record a request asks for: an email that waits, when
// it is to be held, or a message that is sending. Its claim is Started
// with the record's id, Suppressed or Full.
func place(ctx context.Context, tx pgx.Tx, s store.Start) (store.Claim, error) {
	if s.Hold != nil {
		id, err := hold(ctx, tx, s)
		if id == "" {
			return store.Claim{Outcome: store.Full}, err
		}
		return store.Claim{Outcome: store.Started, ID: id}, err
	}
	begin := insertMessage
	if s.ID != "" {
		begin = resume
	}
	id, kept, dropped, err := begin(ctx, tx, s)
	if id == "" {
		return store.Claim{Outcome: store.Suppressed}, err
	}
	return store.Claim{Outcome: store.Started, ID: id, Recipients: kept, Dropped: dropped}, err
}

// keyState is what a key is once its request is placed: an email that
// waits is asked for and done with, one that is sending is in flight
// until its answer comes.
func keyState(s store.Start) string {
	if s.Hold != nil {
		return "done"
	}
	return "in_flight"
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
		// Who was left out the first time is said again.
		c := store.Claim{Outcome: store.Replay, ID: messageID}
		err := tx.QueryRow(ctx, `SELECT left_out FROM messages WHERE id = $1::uuid`, messageID).Scan(&c.Dropped)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil // an email that waits: nobody was left out yet
		}
		return c, err
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
	made, err := place(ctx, tx, s)
	if err != nil || made.Outcome == store.Full {
		return made, err
	}
	if made.Outcome == store.Suppressed {
		_, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE api_key_id = $1::uuid AND key = $2`, s.KeyID, s.IdemKey)
		if inDoubt {
			made.ID = old
		}
		return made, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE idempotency_keys
		SET fingerprint = $3, message_id = $4::uuid, state = $5, created_at = now(), tried_at = now()
		WHERE api_key_id = $1::uuid AND key = $2`, s.KeyID, s.IdemKey, s.Fingerprint, made.ID, keyState(s))
	return made, err
}

// claimNew takes a key nobody holds. If another request takes it first, the
// insert waits for that request to commit and then inserts nothing.
func claimNew(ctx context.Context, tx pgx.Tx, s store.Start) (store.Claim, error) {
	made, err := place(ctx, tx, s)
	if err != nil || made.Outcome != store.Started {
		return made, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (api_key_id, key, fingerprint, message_id, state)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5)
		ON CONFLICT (api_key_id, key) DO NOTHING`, s.KeyID, s.IdemKey, s.Fingerprint, made.ID, keyState(s))
	if err != nil {
		return store.Claim{}, err
	}
	if tag.RowsAffected() == 0 {
		return store.Claim{}, errLostRace // rolls back the record just made
	}
	return made, nil
}
