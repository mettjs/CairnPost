package mail

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/store"
)

// senderLook is the most a look at a sending domain's DNS may take
// after a refusal. It is short: the caller is waiting for its answer,
// and that answer has its own deadline.
const senderLook = 3 * time.Second

// Readiness looks at a sending domain's DNS for the records the mail
// provider asks of it.
type Readiness interface {
	// NotSetUp answers true, and one of the records, when none of the
	// records the provider asks is in place: the domain was never set
	// up with it. It answers false when any is in place and when
	// nothing could be learned: a lookup that failed says nothing.
	NotSetUp(ctx context.Context, domain string) (Record, bool)
}

// Record is a DNS record by its name and type.
type Record struct{ Name, Type string }

// SenderDomain is a send the upstream refused for good, without saying
// why, from a domain none of whose DNS records for the upstream is in
// place: the domain is very likely not set up with it, and then nothing
// from the domain is ever sent, whatever the email. It is a
// driver.ErrRejected: whoever does not ask for it sees an email refused
// for good.
type SenderDomain struct {
	Domain string
	// Record is one of the records looked for and not found in place.
	Record Record
}

func (e *SenderDomain) Error() string {
	return fmt.Sprintf("mail: the upstream refused a send from %s, none of whose DNS records for it is in place", e.Domain)
}

// Unwrap makes a SenderDomain a driver.ErrRejected.
func (*SenderDomain) Unwrap() error { return driver.ErrRejected }

// senderOf turns a refusal the upstream gives for a bad email and for a
// domain it does not know alike into a *SenderDomain when the sender's
// domain has none of the records the upstream asks; any other error is
// answered as it came. It reads DNS, so it is asked once the send is
// settled, and it stops when the caller has gone: the answer is all it
// changes.
func (s *Service) senderOf(ctx context.Context, id, from string, err error) error {
	if s.opt.Domains == nil || !errors.Is(err, driver.ErrSenderUnclear) {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, senderLook)
	defer cancel()
	domain := store.DomainOf(from)
	record, bad := s.opt.Domains.NotSetUp(ctx, domain)
	if !bad {
		return err
	}
	s.log.Warn("mail: the refused send's domain has none of the DNS records the upstream asks; it is likely not set up with it, and then nothing from it is sent",
		"message", id, "domain", domain, "record", record.Name, "type", record.Type)
	return &SenderDomain{Domain: domain, Record: record}
}
