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

// Filler makes an email's words from a template: Find answers the one
// a sending domain's email is made from under a name, FillFrom fills it.
// An error that is a *templates.Invalid says the request is at fault.
type Filler interface {
	Find(ctx context.Context, domain, name string) (store.Template, error)
	FillFrom(t store.Template, values map[string]string) (templates.Filled, error)
}

// found are the templates a batch has looked up, by domain and name:
// each is read once, however many of its emails are made from it.
type found map[[2]string]store.Template

func (f found) find(ctx context.Context, from Filler, domain, name string) (store.Template, error) {
	if t, ok := f[[2]string{domain, name}]; ok {
		return t, nil
	}
	t, err := from.Find(ctx, domain, name)
	if err == nil && f != nil {
		f[[2]string{domain, name}] = t
	}
	return t, err
}

// placeholder stands where a request's words would be when its template
// cannot fill them: the request is then good only as one made before.
const placeholder = "-"

// asked is a request's template as it was asked for: what a repeat of
// the request is compared by, whatever the template says by then.
type asked struct {
	use     Use
	subject string // the subject given with it, "" for the template's
}

// filled is a request with its template's words in place of the
// template: an *Invalid or ErrDomain when it cannot be.
//
// A request under an Idempotency-Key that the template cannot fill (it
// was deleted or changed since) may still be one made before, which is
// answered as it was then: it comes back with no words, what it asked,
// and why it could not be filled, to be asked of the key.
func (s *Service) filled(ctx context.Context, key store.Key, r Request, memo found) (Request, *asked, *Invalid, error) {
	out, was, err := s.fill(ctx, key, r, memo)
	var unfilled *Invalid
	if was != nil && errors.As(err, &unfilled) && (r.IdemKey != "" || r.keyed) {
		if r.Subject == "" {
			r.Subject = placeholder
		}
		r.Text, r.Template = placeholder, nil
		return r, was, unfilled, nil
	}
	return out, was, nil, err
}

func (s *Service) fill(ctx context.Context, key store.Key, r Request, memo found) (Request, *asked, error) {
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
	was := &asked{use: *r.Template, subject: r.Subject}
	t, err := memo.find(ctx, s.opt.Templates, domain, r.Template.Name)
	var got templates.Filled
	if err == nil {
		got, err = s.opt.Templates.FillFrom(t, r.Template.Variables)
	}
	var bad *templates.Invalid
	if errors.As(err, &bad) {
		return r, was, &Invalid{"template", bad.Reason}
	}
	if err != nil {
		return r, nil, err
	}
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
