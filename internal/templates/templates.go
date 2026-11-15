// Package templates keeps the emails written once to be sent many
// times, and fills one for a send. It says what a template may be,
// whose it is, and what a send must give it.
package templates

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/store"
)

// ErrDomain is a key bound to one sending domain asking for a template
// of another.
var ErrDomain = errors.New("templates: not this key's sending domain")

// Invalid is a template, or a use of one, that can never work as it is.
// Field names the part at fault; Reason is a whole sentence. Neither
// quotes what a template or a value says.
type Invalid struct{ Field, Reason string }

func (e *Invalid) Error() string { return "templates: " + e.Reason }

// Options are the limits templates are held to.
type Options struct {
	// Max is how many templates a sending domain may have, and how many
	// may serve every domain; 0 for no limit.
	Max int
	// MaxBytes is how long a template's subject and bodies may be in
	// all, and how long an email filled from one.
	MaxBytes int
}

// mostListed is how many templates one list holds.
const mostListed = 1000

// MaxVariables is how many variables one template may name.
const MaxVariables = 100

var nameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Service is the templates' use cases.
type Service struct {
	store store.Templates
	opt   Options
}

// NewService returns the service over a store.
func NewService(s store.Templates, opt Options) *Service { return &Service{store: s, opt: opt} }

// New is a template to make. Domain is "" for the caller's own, or for
// one that serves every domain when the caller is bound to none.
type New struct {
	Name, Domain, Subject, HTML, Text string
	Defaults                          map[string]string
}

// Create keeps a new template for a caller bound to scope ("" for none).
// An *Invalid, ErrDomain or store.ErrTaken otherwise.
func (s *Service) Create(ctx context.Context, scope string, n New) (store.Template, error) {
	domain, ok := address.Host(n.Domain)
	switch {
	case n.Domain == "":
		domain = scope
	case !ok:
		return store.Template{}, &Invalid{"domain", `"domain" must be a sending domain, such as "mail.example.com", or left out.`}
	case scope != "" && domain != scope:
		return store.Template{}, ErrDomain
	}
	t := store.Template{Domain: domain, Name: n.Name, Subject: n.Subject, HTML: n.HTML, Text: n.Text, Defaults: n.Defaults}
	t.Variables = named(t)
	if err := s.check(t); err != nil {
		return store.Template{}, err
	}
	made, err := s.store.CreateTemplate(ctx, t, s.opt.Max)
	if errors.Is(err, store.ErrLimit) {
		// Counted for the domain it belongs to, so one domain's
		// templates never stand in another's way.
		of := "That sending domain has"
		if domain == "" {
			of = "The templates that serve every domain are"
		}
		return store.Template{}, &Invalid{"name", fmt.Sprintf("%s %d, as many as there may be. Delete one to make room.", of, s.opt.Max)}
	}
	return made, err
}

// List answers the templates a caller manages, each without its
// bodies: its own domain's, or, for a caller bound to none, those of
// the domain asked for or all for "". more says there are others than
// the list holds: narrow it to a domain to see them.
func (s *Service) List(ctx context.Context, scope, domain string) (list []store.Template, more bool, err error) {
	if scope != "" {
		domain = scope
	} else if domain != "" {
		var ok bool
		if domain, ok = address.Host(domain); !ok {
			return nil, false, &Invalid{"domain", `"domain" must be a sending domain, such as "mail.example.com", or left out.`}
		}
	}
	list, err = s.store.ListTemplates(ctx, domain, mostListed+1)
	if len(list) > mostListed {
		list, more = list[:mostListed], true
	}
	return list, more, err
}

// Get answers one a caller manages; store.ErrNotFound for none, and
// for another domain's, which is not told apart.
func (s *Service) Get(ctx context.Context, scope, id string) (store.Template, error) {
	t, err := s.store.Template(ctx, id)
	if err == nil && scope != "" && t.Domain != scope {
		return store.Template{}, store.ErrNotFound
	}
	return t, err
}

// Change is what to write over a template: nil leaves a part as it is.
type Change struct {
	Name, Subject, HTML, Text *string
	Defaults                  *map[string]string
}

// Update changes a template a caller manages. The whole it becomes is
// held to what a new one is. Of two changes made at one time neither
// undoes the other: the later is made over what the earlier left, and
// store.ErrStale says it could not be, for the others kept coming.
func (s *Service) Update(ctx context.Context, scope, id string, c Change) (t store.Template, err error) {
	for range 3 {
		if t, err = s.update(ctx, scope, id, c); !errors.Is(err, store.ErrStale) {
			break
		}
	}
	return t, err
}

func (s *Service) update(ctx context.Context, scope, id string, c Change) (store.Template, error) {
	t, err := s.Get(ctx, scope, id)
	if err != nil {
		return store.Template{}, err
	}
	for _, part := range []struct {
		to   *string
		from *string
	}{{&t.Name, c.Name}, {&t.Subject, c.Subject}, {&t.HTML, c.HTML}, {&t.Text, c.Text}} {
		if part.from != nil {
			*part.to = *part.from
		}
	}
	if c.Defaults != nil {
		t.Defaults = *c.Defaults
	}
	t.Variables = named(t)
	if err := s.check(t); err != nil {
		return store.Template{}, err
	}
	return s.store.UpdateTemplate(ctx, t)
}

// Delete removes a template a caller manages. An email already asked
// for keeps what it was filled with.
func (s *Service) Delete(ctx context.Context, scope, id string) error {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return err
	}
	return s.store.DeleteTemplate(ctx, id)
}
