package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/store"
)

// hold is a checked email as it is kept to be sent later: what it says
// and its files. Who it goes to and from whom are kept beside, in the
// open, as a sent email's are.
func hold(c checked, room int64) *store.Hold {
	h := &store.Hold{SendAt: c.at, Room: room}
	for _, f := range c.msg.Attachments {
		h.Files = append(h.Files, store.HeldFile{Filename: f.Filename, Type: f.Type, ContentID: f.ContentID, Content: f.Content})
	}
	// Strings and a map of strings always encode.
	h.Words, _ = json.Marshal(Words{FromName: c.msg.FromName, ReplyTo: c.msg.ReplyTo, Subject: c.msg.Subject,
		HTML: c.msg.HTML, Text: c.msg.Text, Headers: c.msg.Headers})
	return h
}

// ErrGone: an email that waited is not the caller's to send: it was
// cancelled, taken again by another, or handed to the mail provider
// already.
var ErrGone = errors.New("mail: the email is not this sender's to send")

// heldTimeout bounds reading a waiting email, with its files, and
// recording that it is being sent.
const heldTimeout = 30 * time.Second

// movedTo is when an email asked for again will be sent: the time it
// waits for now, which may have been moved since, or the time asked for
// when it waits no more.
func (s *Service) movedTo(ctx context.Context, id string, asked time.Time) time.Time {
	if asked.IsZero() {
		return asked
	}
	if h, err := s.held.Held(ctx, id); err == nil {
		return h.SendAt
	}
	return asked
}

// SendHeld sends an email whose time has come, which the caller took
// from those that wait. again says that if the mail provider is not
// taking mail now, the email will be tried again.
//
// Its error is a driver's ErrRejected or ErrUnavailable, ErrInDoubt,
// ErrSuppressed when nobody it is addressed to can be sent to any more,
// ErrGone, or a failure of our own before anything was sent.
func (s *Service) SendHeld(ctx context.Context, h store.Held, again bool) error {
	// Reading it and recording the send are bounded, as the send is.
	reading, cancel := context.WithTimeout(ctx, heldTimeout)
	defer cancel()
	raw, files, err := s.held.HeldWords(reading, h.ID)
	if err != nil {
		return fmt.Errorf("mail: read a waiting email: %w", err)
	}
	if raw == nil {
		return ErrGone
	}
	var w Words
	if json.Unmarshal(raw, &w) != nil {
		// The decoder's own error can quote what it choked on.
		return errors.New("mail: what a waiting email says is not what was written")
	}
	msg := driver.Message{From: h.Sender, FromName: w.FromName, ReplyTo: w.ReplyTo, Subject: w.Subject,
		HTML: w.HTML, Text: w.Text, Headers: w.Headers}
	for _, f := range files {
		msg.Attachments = append(msg.Attachments, driver.Attachment{Filename: f.Filename, Type: f.Type, ContentID: f.ContentID, Content: f.Content})
	}
	msg = addressed(msg, h.Recipients)
	claim, err := s.store.Start(reading, store.Start{ID: h.ID, Claim: h.Claim, KeyID: h.KeyID, Sender: h.Sender, Recipients: h.Recipients,
		Driver: s.driver.Name(), Content: s.contentOf(msg), KeepContent: s.opt.KeepContent, Files: s.keptFiles(msg)})
	if err != nil {
		return fmt.Errorf("mail: record the send: %w", err)
	}
	if claim.Outcome == store.Gone {
		return ErrGone
	}
	if claim.Outcome == store.Suppressed {
		s.log.Info("mail: a waiting email was not sent; nobody it is addressed to in the open can be sent to",
			"domain", store.DomainOf(h.Sender), "message", h.ID)
		return ErrSuppressed
	}
	if len(claim.Dropped) > 0 {
		s.log.Info("mail: an email goes to fewer addresses; the others are suppressed for the sending domain",
			"domain", store.DomainOf(h.Sender), "message", h.ID, "left_out", len(claim.Dropped))
	}
	return s.deliver(ctx, h.ID, addressed(msg, claim.Recipients), again)
}

// mayChange says whether a key may cancel or move a waiting email: the
// key that asked for it, and an admin key that may read it.
func mayChange(key store.Key, h store.Held) bool {
	return MayRead(key, store.Message{KeyID: h.KeyID, Sender: h.Sender})
}

// waiting answers, for a key, an email that waits; store.ErrNotFound
// for none and for one the key may not touch, ErrNotScheduled for one
// that was cancelled.
func (s *Service) waiting(ctx context.Context, key store.Key, id string) error {
	h, err := s.held.Held(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// One that was sent is a message, and no longer waits.
		if _, _, merr := s.MessageFor(ctx, key, id); merr == nil {
			return ErrNotScheduled
		}
		return store.ErrNotFound
	case err != nil:
		return err
	case !mayChange(key, h):
		return store.ErrNotFound
	case h.State == store.StateCancelled:
		return ErrNotScheduled
	}
	return nil
}

// Cancel ends the wait of an email that was to be sent later: it will
// not be sent, and what it said is deleted. ErrNotScheduled once it is
// being sent, was sent or was cancelled; store.ErrNotFound for none.
func (s *Service) Cancel(ctx context.Context, key store.Key, id string) error {
	if err := s.waiting(ctx, key, id); err != nil {
		return err
	}
	return late(s.held.Cancel(ctx, id))
}

// Reschedule gives a waiting email another time. Its error is Cancel's,
// or an *Invalid for a time it cannot be sent at.
func (s *Service) Reschedule(ctx context.Context, key store.Key, id string, at time.Time) error {
	switch now := s.opt.Now(); {
	case !at.After(now):
		return &Invalid{"scheduled_at", `"scheduled_at" must be a time to come.`}
	case at.Sub(now) > s.opt.ScheduleAhead:
		return &Invalid{"scheduled_at", fmt.Sprintf(`"scheduled_at" may be at most %s from now.`, span(s.opt.ScheduleAhead))}
	}
	if err := s.waiting(ctx, key, id); err != nil {
		return err
	}
	return late(s.held.Reschedule(ctx, id, at))
}

// late reads the store's "too late" as this package's.
func late(err error) error {
	if errors.Is(err, store.ErrTooLate) {
		return ErrNotScheduled
	}
	return err
}

// asMessage is an email that waits, as an email's record is answered:
// no message yet, so each address stands as the email does.
func asMessage(h store.Held) store.Message {
	m := store.Message{ID: h.ID, EmailID: h.ID, KeyID: h.KeyID, Sender: h.Sender, State: h.State,
		CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt, SendAt: h.SendAt, Reason: h.Reason}
	for i, r := range h.Recipients {
		if i == 0 {
			m.Recipient, m.Kind = r.Address, r.Kind
		}
		m.Recipients = append(m.Recipients, store.Recipient{Address: r.Address, Kind: r.Kind, State: h.State, UpdatedAt: h.UpdatedAt})
	}
	return m
}

// Scheduled lists, for an admin key, the emails that wait to be sent,
// soonest first: those due after a time, of the key's domain if it is
// bound to one. None for a key that may only send.
func (s *Service) Scheduled(ctx context.Context, key store.Key, after time.Time, limit int) ([]store.Held, error) {
	if key.Scope != store.ScopeAdmin {
		return nil, nil
	}
	return s.held.ListHeld(ctx, key.Domain, after, min(max(limit, 1), mostListed))
}

// heldContent answers what a waiting email says, as content is shown:
// kept until it is sent.
func (s *Service) heldContent(ctx context.Context, id string) (*Content, error) {
	h, err := s.held.Held(ctx, id)
	if err != nil || h.State != store.StateScheduled {
		return nil, ignoreNotFound(err)
	}
	raw, files, err := s.held.HeldWords(ctx, id)
	if err != nil || raw == nil {
		return nil, err
	}
	c := Content{KeptUntil: h.SendAt.UTC()}
	if json.Unmarshal(raw, &c.Words) != nil {
		return nil, errors.New("mail: what a waiting email says is not what was written")
	}
	for _, f := range files {
		c.Attachments = append(c.Attachments, File{Filename: f.Filename, ContentType: f.Type, ContentID: f.ContentID, Size: len(f.Content), Kept: true})
	}
	return &c, nil
}

func ignoreNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// Waiting counts the emails that wait to be sent, of a sending domain
// or of all for "".
func (s *Service) Waiting(ctx context.Context, domain string) (int, error) {
	return s.held.Waiting(ctx, domain)
}
