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
}

// What an email may be unless the operator says otherwise: how many
// addresses it goes to, how many files it carries and their size in all.
const (
	DefaultMaxRecipients      = 50
	DefaultMaxAttachments     = 10
	DefaultMaxAttachmentBytes = 10 << 20
)

// Receipt is what a send answers: the email's id, and the addresses it
// was not sent to because they are suppressed.
type Receipt struct {
	ID         string
	Suppressed []string
}

// Store is what the service keeps its records in.
type Store interface {
	store.Mail
	store.Events
}

// Service sends mail.
type Service struct {
	store  store.Mail
	events store.Events
	driver driver.Driver
	opt    Options
	log    *slog.Logger
}

// New returns the service.
func New(s Store, d driver.Driver, opt Options, log *slog.Logger) *Service {
	if opt.MaxRecipients == 0 {
		opt.MaxRecipients = DefaultMaxRecipients
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
	return &Service{store: s, events: s, driver: d, opt: opt, log: log}
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
	c, err := check(r, s.opt)
	if err != nil {
		return Receipt{}, err
	}
	if key.Domain != "" && key.Domain != c.domain {
		return Receipt{}, ErrDomain
	}
	start := store.Start{
		KeyID: key.ID, Sender: c.msg.From, Recipients: addressees(c.msg), Driver: s.driver.Name(),
		IdemKey: r.IdemKey, TTL: s.opt.IdempotencyTTL, InDoubt: s.opt.InDoubtWindow,
		Content: s.contentOf(c.msg), KeepContent: s.opt.KeepContent,
	}
	if r.IdemKey != "" {
		start.Fingerprint = fingerprint(presented, c.msg)
	}
	claim, err := s.store.Start(ctx, start)
	if err != nil {
		return Receipt{}, fmt.Errorf("mail: record the send: %w", err)
	}
	switch claim.Outcome {
	case store.Replay:
		return Receipt{ID: claim.ID, Suppressed: claim.Dropped}, nil
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
	msg := c.msg
	if len(claim.Dropped) > 0 {
		s.log.Info("mail: an email goes to fewer addresses; the others are suppressed for the sending domain",
			"domain", c.domain, "message", claim.ID, "left_out", len(claim.Dropped))
		msg = addressed(msg, claim.Recipients)
	}
	return Receipt{ID: claim.ID, Suppressed: claim.Dropped}, s.deliver(ctx, claim.ID, msg)
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
func (s *Service) deliver(ctx context.Context, id string, m driver.Message) error {
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
		if ferr := s.store.Failed(record, id); ferr != nil {
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
