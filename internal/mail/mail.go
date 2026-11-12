// Package mail is the send use case: check the request, claim its
// Idempotency-Key, hand it to the driver, record what became of it.
package mail

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
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
)

// Options are the service's settings.
type Options struct {
	// SendTimeout bounds one call to the driver.
	SendTimeout time.Duration
	// IdempotencyTTL and InDoubtWindow are store.Start's.
	IdempotencyTTL, InDoubtWindow time.Duration
}

// Service sends mail.
type Service struct {
	store  store.Mail
	driver driver.Driver
	opt    Options
	log    *slog.Logger
}

// New returns the service.
func New(s store.Mail, d driver.Driver, opt Options, log *slog.Logger) *Service {
	return &Service{store: s, driver: d, opt: opt, log: log}
}

// Send sends one email and answers its id. presented is the caller's API
// key as it arrived: the request's fingerprint is keyed by it.
//
// An error is an *Invalid, ErrDomain, ErrInFlight, ErrMismatch, or the
// driver's (see driver.Driver for its three kinds).
func (s *Service) Send(ctx context.Context, key store.Key, presented string, r Request) (string, error) {
	c, err := check(r)
	if err != nil {
		return "", err
	}
	if key.Domain != "" && key.Domain != c.domain {
		return "", ErrDomain
	}
	start := store.Start{
		KeyID: key.ID, Sender: c.sender, Recipient: c.recipient, Driver: s.driver.Name(),
		IdemKey: r.IdemKey, TTL: s.opt.IdempotencyTTL, InDoubt: s.opt.InDoubtWindow,
	}
	if r.IdemKey != "" {
		start.Fingerprint = fingerprint(presented, c.msg)
	}
	outcome, id, err := s.store.Start(ctx, start)
	if err != nil {
		return "", fmt.Errorf("mail: record the send: %w", err)
	}
	switch outcome {
	case store.Replay:
		return id, nil
	case store.InFlight:
		return "", ErrInFlight
	case store.Mismatch:
		return "", ErrMismatch
	}
	return id, s.deliver(ctx, id, c.msg)
}

// deliver hands a started message to the driver and records the answer. The
// record is written even if the caller has gone: the email's fate does not
// depend on who was waiting for it.
func (s *Service) deliver(ctx context.Context, id string, m driver.Message) error {
	sendCtx, cancel := context.WithTimeout(ctx, s.opt.SendTimeout)
	upstreamID, err := s.driver.Send(sendCtx, m)
	cancel()
	record := context.WithoutCancel(ctx)
	switch {
	case err == nil:
		if err := s.store.Sent(record, id, upstreamID); err != nil {
			// The email went. Saying otherwise would invite a second one.
			s.log.Error("mail: an email was sent and its record could not be updated", "message", id, "error", err)
		}
		return nil
	case errors.Is(err, driver.ErrRejected), errors.Is(err, driver.ErrUnavailable):
		if ferr := s.store.Failed(record, id); ferr != nil {
			s.log.Error("mail: a refused send's record could not be updated", "message", id, "error", ferr)
		}
		return err
	default:
		// The answer was lost: the message stays "sending" and its key in
		// flight, until the in-doubt window lets a retry through.
		s.log.Warn("mail: the upstream's answer was lost; the email may have gone", "message", id)
		return fmt.Errorf("mail: the upstream's answer was lost: %w", err)
	}
}

// Message answers a message's record; store.ErrNotFound for none.
func (s *Service) Message(ctx context.Context, id string) (store.Message, error) {
	return s.store.Message(ctx, id)
}

// fingerprint is a digest of a request keyed by the caller's API key, which
// is kept nowhere: whoever reads the digest cannot test a guessed body
// against it.
func fingerprint(presented string, m driver.Message) []byte {
	// encoding/json writes a struct's fields in order and a map's keys
	// sorted, so equal requests encode equally.
	raw, _ := json.Marshal(m)
	mac := hmac.New(sha256.New, []byte(presented))
	mac.Write(raw)
	return mac.Sum(nil)
}
