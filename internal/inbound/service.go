package inbound

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/store"
)

// ErrOff is a server that takes no emails: it holds no secret to know
// who hands one on.
var ErrOff = errors.New("inbound: this server takes no emails")

// ErrFull is the emails kept having no room for one more.
var ErrFull = errors.New("inbound: the emails kept hold as much as there may be")

// ErrTooLarge is an email longer than this server takes.
var ErrTooLarge = errors.New("inbound: the email is longer than this server takes")

// ErrAddress is an email handed on for something that is no address.
var ErrAddress = errors.New("inbound: what it came for is not an address")

// Defaults of the settings.
const (
	DefaultKeep      = 7 * 24 * time.Hour
	DefaultMaxBytes  = 25 << 20
	DefaultRoom      = 1 << 30
	DefaultTolerance = 5 * time.Minute
	// mostListed is how many emails one page of a list holds.
	mostListed = 100
	// maxShown is how long a subject or a sender's name is kept for a list.
	maxShown = 998
	// maxAddress is the longest address there can be.
	maxAddress = 320
)

// Options are the settings.
type Options struct {
	// Secret is what whoever hands emails on signs with; "" takes none.
	Secret string
	// Keep is how long an email is kept; MaxBytes how long one may be;
	// Room how many bytes the emails kept may be in all.
	Keep     time.Duration
	MaxBytes int64
	Room     int64
	// Tolerance is how far from now a hand-over's time may be.
	Tolerance time.Duration
	Now       func() time.Time
}

// Service takes the emails that come and reads them for whoever may.
type Service struct {
	store store.Inbounds
	opt   Options
}

// New returns the service over a store.
func New(s store.Inbounds, opt Options) *Service {
	if opt.Keep == 0 {
		opt.Keep = DefaultKeep
	}
	if opt.MaxBytes == 0 {
		opt.MaxBytes = DefaultMaxBytes
	}
	if opt.Tolerance == 0 {
		opt.Tolerance = DefaultTolerance
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Service{store: s, opt: opt}
}

// On says whether this server takes emails.
func (s *Service) On() bool { return s.opt.Secret != "" }

// MaxBytes is how long an email may be.
func (s *Service) MaxBytes() int64 { return s.opt.MaxBytes }

// Admit says whether a hand-over may be taken, before any of its body
// is read: ErrOff, ErrUnsigned or ErrAddress otherwise. The signature
// is asked first, so a stranger learns nothing else.
func (s *Service) Admit(e Envelope) error {
	if !s.On() {
		return ErrOff
	}
	if err := Verify(s.opt.Secret, e, s.opt.Now(), s.opt.Tolerance); err != nil {
		return err
	}
	if _, _, ok := recipient(e.To); !ok {
		return ErrAddress
	}
	return nil
}

// recipient reads the address an email came for into the form it is
// kept in, with its domain: false for what is no address of a domain
// CairnPost could send from. The domain is held to plain ASCII before
// it is folded, so no letter that only looks like another folds into
// another domain's name.
func recipient(written string) (to, domain string, ok bool) {
	said, ok := Said(written)
	if !ok || len(said) > maxAddress {
		return "", "", false
	}
	if to, ok = address.Bare(said); !ok {
		return "", "", false
	}
	at := strings.LastIndex(to, "@")
	for i := at + 1; i < len(to); i++ {
		if to[i] >= 0x80 {
			return "", "", false
		}
	}
	if domain, ok = address.Host(to[at+1:]); !ok {
		return "", "", false
	}
	return store.Fold(to), domain, true
}

// sender reads who an email came from, as the sending server said: ""
// for nobody, which is what a mail server's own notice comes from. It
// is a stranger's word: kept as an address where it is one, and
// otherwise as it was said, on one line and of a length.
func sender(written string) string {
	said, ok := Said(written)
	if said = strings.TrimSpace(said); !ok || said == "" || said == "<>" {
		return ""
	}
	if from, ok := address.Bare(said); ok && len(from) <= maxAddress {
		return from
	}
	said = shown(said)
	if len(said) > maxAddress {
		said = strings.ToValidUTF8(said[:maxAddress], "")
	}
	return said
}

// shown is a header's value as a list keeps it: on one line, of a
// length, with nothing a database cannot hold.
func shown(v string) string {
	v = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, strings.ToValidUTF8(v, "�"))
	if len(v) > maxShown {
		v = strings.ToValidUTF8(v[:maxShown], "")
	}
	return strings.TrimSpace(v)
}

// Receive keeps an email handed on with an envelope Admit took. raw is
// the email as it came. The same email for the same address is kept
// once: again is then true, and the id the first one's. ErrUnsigned for
// a body the envelope does not name, ErrTooLarge, ErrFull.
func (s *Service) Receive(ctx context.Context, e Envelope, raw []byte) (id string, again bool, err error) {
	if err := s.Admit(e); err != nil {
		return "", false, err
	}
	if int64(len(raw)) > s.opt.MaxBytes {
		return "", false, ErrTooLarge
	}
	digest := sha256.Sum256(raw)
	if !Matches(e, digest) {
		return "", false, ErrUnsigned
	}
	to, domain, _ := recipient(e.To)
	read := Parse(raw)
	id, again, err = s.store.Receive(ctx, store.NewInbound{
		Inbound: store.Inbound{Domain: domain, Recipient: to, Sender: sender(e.From),
			Subject: shown(read.Subject), From: shown(read.From), Files: len(read.Attachments), Partial: read.Partial},
		Digest: digest[:], Raw: raw, Keep: s.opt.Keep, Room: s.opt.Room,
	})
	if errors.Is(err, store.ErrLimit) {
		return "", false, ErrFull
	}
	return id, again, err
}

// reads says whether a key is shown the emails that came for a domain:
// an admin key, of that domain if it is bound to one.
func reads(key store.Key, domain string) bool {
	return key.Scope == store.ScopeAdmin && (key.Domain == "" || key.Domain == domain)
}

// List answers a page of the emails kept that a key may read, newest
// first: those after the cursor given, "" for the newest. next is the
// cursor of the page that follows, "" when this is the last. None for
// a key that reads none.
func (s *Service) List(ctx context.Context, key store.Key, after string, limit int) (list []store.Inbound, next string, err error) {
	if key.Scope != store.ScopeAdmin {
		return nil, "", nil
	}
	if limit <= 0 || limit > mostListed {
		limit = mostListed
	}
	list, err = s.store.ListInbound(ctx, key.Domain, after, limit+1)
	if len(list) > limit {
		list, next = list[:limit], list[limit-1].ID
	}
	return list, next, err
}

// Get answers one email a key may read; store.ErrNotFound for none, and
// for one it may not, which is not told apart.
func (s *Service) Get(ctx context.Context, key store.Key, id string) (store.Inbound, error) {
	in, err := s.store.Inbound(ctx, id)
	if err == nil && !reads(key, in.Domain) {
		return store.Inbound{}, store.ErrNotFound
	}
	return in, err
}

// Raw answers an email as it came, for a key that may read it.
func (s *Service) Raw(ctx context.Context, key store.Key, id string) (store.Inbound, []byte, error) {
	in, err := s.Get(ctx, key, id)
	if err != nil {
		return store.Inbound{}, nil, err
	}
	raw, err := s.store.InboundRaw(ctx, id)
	return in, raw, err
}

// Read answers an email as it reads, for a key that may read it.
func (s *Service) Read(ctx context.Context, key store.Key, id string) (store.Inbound, Read, error) {
	in, raw, err := s.Raw(ctx, key, id)
	if err != nil {
		return store.Inbound{}, Read{}, err
	}
	return in, Parse(raw), nil
}

// FileOf answers the file at a place of an email, from 1, with what it
// holds; store.ErrNotFound for none.
func (s *Service) FileOf(ctx context.Context, key store.Key, id string, place int) (Part, []byte, error) {
	_, raw, err := s.Raw(ctx, key, id)
	if err != nil {
		return Part{}, nil, err
	}
	part, content, ok := File(raw, place)
	if !ok {
		return Part{}, nil, store.ErrNotFound
	}
	return part, content, nil
}

// Delete deletes an email a key may read, before its time.
func (s *Service) Delete(ctx context.Context, key store.Key, id string) error {
	if _, err := s.Get(ctx, key, id); err != nil {
		return err
	}
	return s.store.DeleteInbound(ctx, id)
}

// Kept counts the emails kept for a domain, or for all for "".
func (s *Service) Kept(ctx context.Context, domain string) (int, error) {
	return s.store.CountInbound(ctx, domain)
}

// KeptFor is how long an email is kept.
func (s *Service) KeptFor() time.Duration { return s.opt.Keep }
