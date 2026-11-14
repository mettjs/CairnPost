// Package mail is the send use case: check the request, claim its
// Idempotency-Key, hand it to the driver, record what became of it.
package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/store"
)

// What a send can answer besides an *Invalid and the driver's own errors.
var (
	// ErrDomain: the key is bound to another sending domain.
	ErrDomain = errors.New("mail: this API key may not send from that domain")
	// ErrInFlight: the same Idempotency-Key is being sent now, or its last
	// try has no answer yet. Trying again shortly is right.
	ErrInFlight = errors.New("mail: a send under this Idempotency-Key is still in flight")
	// ErrMismatch: the Idempotency-Key was used with a different request,
	// which was sent. This one never will be.
	ErrMismatch = errors.New("mail: this Idempotency-Key was used with a different request")
	// ErrSuppressed: every recipient is on the sending domain's
	// suppression list: it bounced for good, complained, or was put there
	// by hand. Nothing is sent, and nothing will be until a suppression is
	// lifted. An email with other recipients goes to those.
	ErrSuppressed = errors.New("mail: nobody the email is addressed to in the open can be sent to: each is suppressed for this sending domain")
	// ErrScheduleFull: the email was to be sent later, and the emails
	// that wait hold as much as the server is set to keep. Nothing was
	// kept.
	ErrScheduleFull = errors.New("mail: the emails waiting to be sent hold as much as they may")
	// ErrNotScheduled: the email does not wait to be sent (any more).
	ErrNotScheduled = errors.New("mail: the email does not wait to be sent")
	// ErrInDoubt: the upstream's answer was lost, so the email may have
	// gone. It is the only error after which that is so: every other one
	// means nothing was sent.
	ErrInDoubt = errors.New("mail: the upstream's answer was lost; the email may have gone")
)

// Busy is an ErrInFlight that knows how long until a retry is let through.
type Busy struct{ After time.Duration }

func (*Busy) Error() string { return ErrInFlight.Error() }

// Unwrap makes a Busy an ErrInFlight.
func (*Busy) Unwrap() error { return ErrInFlight }

// recordTimeout bounds writing down what became of a send.
const recordTimeout = 5 * time.Second

// Options are the service's settings.
type Options struct {
	// SendTimeout bounds one call to the driver.
	SendTimeout time.Duration
	// IdempotencyTTL and InDoubtWindow are store.Start's.
	IdempotencyTTL, InDoubtWindow time.Duration
	// KeepContent is how long what an email said is kept, for whoever
	// runs CairnPost to see what was sent; 0, the rule, keeps none.
	KeepContent time.Duration
	// MaxRecipients is how many addresses one email may go to, counting
	// "to", "cc" and "bcc"; 0 is DefaultMaxRecipients.
	MaxRecipients int
	// MaxAttachments is how many files one email may carry, and
	// MaxAttachmentBytes how large they may be in all; 0 is the default
	// of each. A negative MaxAttachments takes none.
	MaxAttachments     int
	MaxAttachmentBytes int64
	// MaxBatch is how many emails one batch may hold, BatchParallel how
	// many of them are sent at one time, and BatchTime how long a batch
	// goes on starting emails; 0 is the default of each.
	MaxBatch      int
	BatchParallel int
	BatchTime     time.Duration
	// ScheduleAhead is how far ahead an email may be asked for, and
	// ScheduleRoom how many bytes the emails that wait may hold in all;
	// 0 is the default of each, a negative ScheduleRoom no bound.
	ScheduleAhead time.Duration
	ScheduleRoom  int64
	// Now is the clock a time asked for is held against; nil is the
	// wall clock.
	Now func() time.Time
	// Stopping is closed when the server is told to stop: a batch then
	// starts no further email. nil never stops one.
	Stopping <-chan struct{}
	// noInline is the driver saying it cannot send a file the HTML
	// shows; New asks it.
	noInline bool
}

// What an email may be unless the operator says otherwise: how many
// addresses it goes to, how many files it carries and their size in all.
const (
	DefaultMaxRecipients      = 50
	DefaultMaxAttachments     = 10
	DefaultMaxAttachmentBytes = 10 << 20
	// And a batch: how many emails, how many at one time, for how long.
	DefaultMaxBatch      = 100
	DefaultBatchParallel = 4
	DefaultBatchTime     = time.Minute
	// And what may wait: how far ahead, and how much in all.
	DefaultScheduleAhead = 30 * 24 * time.Hour
	DefaultScheduleRoom  = 1 << 30
)

// Receipt is what a send answers: the email's id, and the addresses it
// was not sent to because they are suppressed.
type Receipt struct {
	ID         string
	Suppressed []string
	// ScheduledAt is when it will be sent, for an email that was not
	// sent but put to wait; zero for one that was sent.
	ScheduledAt time.Time
}

// Store is what the service keeps its records in.
type Store interface {
	store.Mail
	store.Events
	store.Schedule
}

// Service sends mail.
type Service struct {
	store  store.Mail
	events store.Events
	held   store.Schedule
	driver driver.Driver
	opt    Options
	log    *slog.Logger
}

// New returns the service.
func New(s Store, d driver.Driver, opt Options, log *slog.Logger) *Service {
	if opt.MaxRecipients == 0 {
		opt.MaxRecipients = DefaultMaxRecipients
	}
	if plain, ok := d.(driver.Plain); ok {
		opt.noInline = plain.NoInlineFiles()
	}
	switch {
	case opt.MaxAttachments == 0:
		opt.MaxAttachments = DefaultMaxAttachments
	case opt.MaxAttachments < 0:
		opt.MaxAttachments = 0
	}
	if opt.MaxAttachmentBytes == 0 {
		opt.MaxAttachmentBytes = DefaultMaxAttachmentBytes
	}
	if opt.MaxBatch == 0 {
		opt.MaxBatch = DefaultMaxBatch
	}
	if opt.BatchParallel <= 0 {
		opt.BatchParallel = DefaultBatchParallel
	}
	if opt.BatchTime == 0 {
		opt.BatchTime = DefaultBatchTime
	}
	if opt.ScheduleAhead == 0 {
		opt.ScheduleAhead = DefaultScheduleAhead
	}
	switch {
	case opt.ScheduleRoom == 0:
		opt.ScheduleRoom = DefaultScheduleRoom
	case opt.ScheduleRoom < 0:
		opt.ScheduleRoom = 0
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Service{store: s, events: s, held: s, driver: d, opt: opt, log: log}
}

// Send sends one email and answers its id. presented is the caller's API
// key as it arrived: the request's fingerprint is keyed by it.
//
// An address that is suppressed is left out and named in the receipt;
// the email goes to the others. With nobody left in "to" or "cc" it is
// ErrSuppressed: a hidden copy alone is not sent.
//
// An error is an *Invalid, ErrDomain, ErrSuppressed, a *Busy (ErrInFlight), ErrMismatch,
// a driver's ErrRejected or ErrUnavailable, ErrInDoubt, or a failure of
// our own before anything was sent. The id comes back beside an error
// whenever the send got far enough to have one; beside ErrSuppressed it is
// an earlier try under the same key that was left in doubt, if there was
// one.
func (s *Service) Send(ctx context.Context, key store.Key, presented string, r Request) (Receipt, error) {
	c, err := s.checked(key, r)
	if err != nil {
		return Receipt{}, err
	}
	return s.send(ctx, key, presented, r.IdemKey, c)
}

// checked is a request this key may send, found sendable: an *Invalid
// or ErrDomain otherwise.
func (s *Service) checked(key store.Key, r Request) (checked, error) {
	c, err := check(r, s.opt)
	if err != nil {
		return checked{}, err
	}
	if key.Domain != "" && key.Domain != c.domain {
		return checked{}, ErrDomain
	}
	return c, nil
}

// send is Send for a request already checked.
func (s *Service) send(ctx context.Context, key store.Key, presented, idem string, c checked) (Receipt, error) {
	start := store.Start{
		KeyID: key.ID, Sender: c.msg.From, Recipients: addressees(c.msg), Driver: s.driver.Name(),
		IdemKey: idem, TTL: s.opt.IdempotencyTTL, InDoubt: s.opt.InDoubtWindow,
		Content: s.contentOf(c.msg), KeepContent: s.opt.KeepContent,
	}
	if idem != "" {
		start.Fingerprint = fingerprint(presented, c.msg, c.at)
	}
	if !c.at.IsZero() {
		// Kept to be sent later: what it says is kept with it, whether
		// or not what emails say is kept once they are sent.
		start.Hold, start.Content = hold(c, s.opt.ScheduleRoom), nil
	}
	claim, err := s.store.Start(ctx, start)
	if err != nil {
		return Receipt{}, fmt.Errorf("mail: record the send: %w", err)
	}
	switch claim.Outcome {
	case store.Replay:
		return Receipt{ID: claim.ID, Suppressed: claim.Dropped, ScheduledAt: c.at}, nil
	case store.Full:
		return Receipt{}, ErrScheduleFull
	case store.InFlight:
		return Receipt{}, &Busy{After: claim.RetryAfter}
	case store.Mismatch:
		return Receipt{}, ErrMismatch
	case store.Suppressed:
		// Said to the operator, without an address: a person who stops
		// getting mail should be findable from the log's side too.
		s.log.Info("mail: a send was refused; nobody it is addressed to in the open can be sent to",
			"domain", c.domain, "earlier_try", claim.ID)
		// The id, if any, is an earlier try under the key whose answer
		// never came: that email may have gone.
		return Receipt{ID: claim.ID}, ErrSuppressed
	}
	if !c.at.IsZero() {
		return Receipt{ID: claim.ID, ScheduledAt: c.at}, nil
	}
	msg := c.msg
	if len(claim.Dropped) > 0 {
		s.log.Info("mail: an email goes to fewer addresses; the others are suppressed for the sending domain",
			"domain", c.domain, "message", claim.ID, "left_out", len(claim.Dropped))
		msg = addressed(msg, claim.Recipients)
	}
	return Receipt{ID: claim.ID, Suppressed: claim.Dropped}, s.deliver(ctx, claim.ID, msg, false)
}

// addressed is a message that goes to the addresses the store kept, each
// as the kind it was recorded as.
func addressed(m driver.Message, kept []store.Addressee) driver.Message {
	m.To, m.Cc, m.Bcc = nil, nil, nil
	for _, a := range kept {
		switch a.Kind {
		case store.KindTo:
			m.To = append(m.To, a.Address)
		case store.KindCc:
			m.Cc = append(m.Cc, a.Address)
		default:
			m.Bcc = append(m.Bcc, a.Address)
		}
	}
	return m
}

// deliver hands a started message to the driver and records the answer.
// Neither step stops because the caller hung up: an upstream call cut off
// midway is an email that may have gone with nobody knowing, so it runs to
// its own timeout, and the record is written within its own.
//
// again says that a send the upstream does not take now will be tried
// again: its Idempotency-Key then stays as it is.
func (s *Service) deliver(ctx context.Context, id string, m driver.Message, again bool) error {
	free := context.WithoutCancel(ctx)
	sendCtx, cancel := context.WithTimeout(free, s.opt.SendTimeout)
	upstreamID, err := s.driver.Send(sendCtx, m)
	cancel()
	record, done := context.WithTimeout(free, recordTimeout)
	defer done()
	switch {
	case err == nil:
		if err := s.store.Sent(record, id, upstreamID); err != nil {
			// The email went. Saying otherwise would invite a second one.
			s.log.Error("mail: an email was sent and its record could not be updated", "message", id, "error", err)
		}
		return nil
	case errors.Is(err, driver.ErrRejected), errors.Is(err, driver.ErrUnavailable):
		// The caller is told only the kind. The cause, which is the
		// upstream's status and nothing of the email, is for the operator.
		s.log.Warn("mail: the upstream refused a send; nothing was sent", "message", id, "error", err)
		failed := s.store.Failed
		if again && errors.Is(err, driver.ErrUnavailable) {
			failed = s.store.Unsent
		}
		if ferr := failed(record, id); ferr != nil {
			s.log.Error("mail: a refused send's record could not be updated", "message", id, "error", ferr)
		}
		return err
	default:
		// The answer was lost. The message's fate is unknown, and its key
		// stays in flight until the in-doubt window lets a retry through.
		s.log.Warn("mail: the upstream's answer was lost; the email may have gone", "message", id, "error", err)
		if lerr := s.store.Lost(record, id); lerr != nil {
			s.log.Error("mail: a lost answer could not be recorded", "message", id, "error", lerr)
		}
		return ErrInDoubt
	}
}
