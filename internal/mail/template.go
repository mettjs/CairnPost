package mail

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash"
	netmail "net/mail"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/templates"
)

// Use is a template to make an email from, and the values for its
// variables. A value is what the email says: never logged, never said
// in an error.
type Use struct {
	Name      string
	Variables map[string]string
}

// Filler makes an email's words from a template.
type Filler interface {
	Fill(ctx context.Context, domain, name string, values map[string]string) (templates.Filled, error)
}

// asked is a request's template as it was asked for: what a repeat of
// the request is compared by, whatever the template says by then.
type asked struct {
	use     Use
	subject string // the subject given with it, "" for the template's
}

// filled is a request with its template's words in place of the
// template: an *Invalid or ErrDomain when it cannot be.
func (s *Service) filled(ctx context.Context, key store.Key, r Request) (Request, *asked, error) {
	if r.HTML != "" || r.Text != "" {
		return r, nil, &Invalid{"template", `With "template", leave out "html" and "text": the template says them.`}
	}
	if s.opt.Templates == nil {
		return r, nil, &Invalid{"template", "This server has no templates."}
	}
	from, err := netmail.ParseAddress(r.From)
	if err != nil {
		_, err := check(r, s.opt) // says what "from" must be
		return r, nil, err
	}
	// Before the template is looked for: a key learns nothing of the
	// templates of a domain it may not send from.
	domain := store.DomainOf(from.Address)
	if key.Domain != "" && key.Domain != domain {
		return r, nil, ErrDomain
	}
	got, err := s.opt.Templates.Fill(ctx, domain, r.Template.Name, r.Template.Variables)
	var bad *templates.Invalid
	if errors.As(err, &bad) {
		return r, nil, &Invalid{"template", bad.Reason}
	}
	if err != nil {
		return r, nil, err
	}
	was := &asked{use: *r.Template, subject: r.Subject}
	if r.Subject == "" {
		r.Subject = got.Subject
	}
	r.HTML, r.Text, r.Template = got.HTML, got.Text, nil
	return r, was, nil
}

// write adds the template a request asked for to its fingerprint: two
// parts, where files come in fours and a time is one, so it reads as
// neither.
func (a *asked) write(mac hash.Hash) {
	// A map's keys are written sorted, so equal values encode equally.
	values, _ := json.Marshal(a.use.Variables)
	for _, part := range [][]byte{[]byte(a.use.Name), values} {
		mac.Write(binary.BigEndian.AppendUint64(nil, uint64(len(part))))
		mac.Write(part)
	}
}
