package templates

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mettjs/cairnpost/internal/fill"
	"github.com/mettjs/cairnpost/internal/store"
)

// Filled is an email's words, made from a template.
type Filled struct{ Subject, HTML, Text string }

// Fill makes what an email of a sending domain says from the template
// of that name: the domain's own, or else the one that serves every
// domain. Each variable takes the value given, or its default; a value
// is written as it is in the subject and the text and escaped in the
// HTML, so it is never markup. An *Invalid for no such template, a
// variable with no value, a value for no variable, and an email that
// would be too long.
func (s *Service) Fill(ctx context.Context, domain, name string, values map[string]string) (Filled, error) {
	t, err := s.Find(ctx, domain, name)
	if err != nil {
		return Filled{}, err
	}
	return s.FillFrom(t, values)
}

// Find answers the template a sending domain's email is made from under
// a name; an *Invalid for none.
func (s *Service) Find(ctx context.Context, domain, name string) (store.Template, error) {
	t, err := s.store.TemplateNamed(ctx, domain, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Template{}, &Invalid{"template", `There is no template of this name for the sending domain in "from".`}
	}
	return t, err
}

// FillFrom is Fill from a template already found.
func (s *Service) FillFrom(t store.Template, values map[string]string) (Filled, error) {
	var err error
	with := map[string]string{}
	var missing []string
	for _, v := range Variables(t) {
		if given, ok := values[v.Name]; ok {
			with[v.Name] = given
		} else if v.Required {
			missing = append(missing, v.Name)
		} else {
			with[v.Name] = v.Default
		}
	}
	if len(missing) > 0 {
		// A variable's name is the template's, never what an email says.
		return Filled{}, &Invalid{"template", fmt.Sprintf(`The template needs a value for %s in "variables".`, quoted(missing))}
	}
	var unknown []string
	for name := range values {
		if _, ok := with[name]; !ok {
			if !fill.Name(name) {
				return Filled{}, &Invalid{"template", `A name in "variables" is letters, digits and "_", and does not start with a digit.`}
			}
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Filled{}, &Invalid{"template", fmt.Sprintf(`The template has no variable %s.`, quoted(unknown))}
	}
	var out Filled
	left := s.opt.MaxBytes
	for _, p := range []struct {
		to   *string
		text string
		as   func(string, map[string]string, int) (string, error)
	}{{&out.Subject, t.Subject, fill.Text}, {&out.HTML, t.HTML, fill.HTML}, {&out.Text, t.Text, fill.Text}} {
		if *p.to, err = p.as(p.text, with, left); err != nil {
			if errors.Is(err, fill.ErrTooLong) {
				return Filled{}, &Invalid{"template", fmt.Sprintf(`Filled, the email is longer than the %d bytes its subject and bodies may be.`, s.opt.MaxBytes)}
			}
			return Filled{}, err
		}
		left -= len(*p.to)
	}
	return out, nil
}

// quoted writes a few names as a sentence has them: "a", "b" and "c".
func quoted(names []string) string {
	const most = 5
	out := ""
	for i, n := range names[:min(len(names), most)] {
		switch {
		case i == 0:
		case i == min(len(names), most)-1 && len(names) <= most:
			out += " and "
		default:
			out += ", "
		}
		out += fmt.Sprintf("%q", n)
	}
	if len(names) > most {
		out += fmt.Sprintf(" and %d more", len(names)-most)
	}
	return out
}
