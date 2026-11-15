package store

import (
	"context"
	"errors"
	"time"
)

// States of an email that waits to be sent. They are not States: an
// email is among the messages only once it is being sent.
const (
	StateScheduled = "scheduled"
	StateCancelled = "cancelled"
)

// Why an email that waited was cancelled by nobody's asking.
const (
	// HeldSuppressed: when its time came, nobody it was addressed to in
	// the open could be sent to.
	HeldSuppressed = "suppressed"
	// HeldKeyRevoked: the API key that asked for it had been revoked.
	HeldKeyRevoked = "key_revoked"
	// HeldFailed: it could not be sent for a fault of this server's,
	// each time it was tried until it was given up on.
	HeldFailed = "failed"
)

// HeldReasons is every reason an email that waited is cancelled for by
// nobody's asking.
var HeldReasons = []string{HeldSuppressed, HeldKeyRevoked, HeldFailed}

// ErrTooLate is an email that no longer waits: it is being sent, was
// sent, or was cancelled.
var ErrTooLate = errors.New("store: the email no longer waits to be sent")

// Held is an email that waits for its time, as it is kept.
type Held struct {
	ID         string
	KeyID      string
	Sender     string
	Recipients []Addressee
	SendAt     time.Time
	// State is StateScheduled, also while a try is under way or another
	// is to come, or StateCancelled.
	State string
	// Reason says why it was cancelled when nobody asked: HeldSuppressed
	// or HeldKeyRevoked; "" otherwise.
	Reason string
	// Tries is how many times it was begun: handed, or about to be, to
	// the driver.
	Tries int
	// Claim names the taking it was answered by (TakeDue): what a taker
	// then does to it, it does under this number, and does nothing once
	// the email was taken again by another.
	Claim     int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HeldFile is a file of an email that waits, with what it holds.
type HeldFile struct {
	Filename, Type, ContentID string
	Content                   []byte
}

// Hold is what Start is given for an email to be sent later: it is kept
// and not started.
type Hold struct {
	SendAt time.Time
	// Words is what the email says, kept as it comes and unread.
	Words []byte
	Files []HeldFile
	// Room is how many bytes the emails that wait may hold in all, this
	// one counted; 0 for no bound.
	Room int64
}

// Schedule keeps the emails that wait to be sent.
type Schedule interface {
	// Held answers an email that waits or was cancelled; ErrNotFound
	// for none, and for one that was sent, which is a Message.
	Held(ctx context.Context, id string) (Held, error)
	// HeldWords answers what a waiting email says and its files; nil
	// when it does not wait any more.
	HeldWords(ctx context.Context, id string) ([]byte, []HeldFile, error)
	// ListHeld lists the emails that wait, soonest first, of a sending
	// domain or of all for "": those due after a time, limit at most.
	ListHeld(ctx context.Context, domain string, after time.Time, limit int) ([]Held, error)
	// Waiting counts the emails that wait, of a domain or of all.
	Waiting(ctx context.Context, domain string) (int, error)
	// Cancel ends an email's wait and deletes what it said and its
	// files, with what a failed try of it kept of its words. ErrTooLate once it is being sent; ErrNotFound for none.
	Cancel(ctx context.Context, id string) error
	// Reschedule gives a waiting email another time; errors as Cancel.
	Reschedule(ctx context.Context, id string, at time.Time) error
	// TakeDue takes, for the caller alone, the emails whose time has
	// come, limit at most, the longest due first. One whose API key was
	// revoked meanwhile is cancelled and not answered.
	TakeDue(ctx context.Context, limit int) ([]Held, error)
	// Postpone hands a taken email back to wait for another try: any
	// address of it still sending is marked as not sent to, since the
	// one who took it says nothing of it is with the upstream. It,
	// Drop and Release are of one taking (Held.Claim): ErrTooLate once
	// the email was taken again or is gone.
	Postpone(ctx context.Context, id string, claim int, wait time.Duration) error
	// Drop cancels a taken email for a reason: it will not be sent.
	Drop(ctx context.Context, id string, claim int, reason string) error
	// Release forgets a taken email, if its record did not already:
	// Sent, Failed and Lost end its wait in the step that settles the
	// message.
	Release(ctx context.Context, id string, claim int) error
	// Recover finds the emails taken, or begun, longer ago than a send
	// can last, by a server that then stopped. One that was being sent
	// is forgotten: its message says what is known, and it is not sent
	// again. One that had not been begun, or whose last try the
	// provider did not take, waits again.
	Recover(ctx context.Context, older time.Duration, limit int) (int, error)
}
