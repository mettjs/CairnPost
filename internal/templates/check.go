package templates

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/mettjs/cairnpost/internal/fill"
	"github.com/mettjs/cairnpost/internal/store"
)

// maxSubject is the longest subject a template may have, as an email's.
const maxSubject = 998

// Variable is one a template names.
type Variable struct {
	Name string
	// Default is what it is filled with when a send gives it no value;
	// Required says it has none, and a send must.
	Default  string
	Required bool
}

// parts are a template's texts by the field each is.
func parts(t store.Template) [3]struct{ field, text string } {
	return [3]struct{ field, text string }{{"subject", t.Subject}, {"html", t.HTML}, {"text", t.Text}}
}

// Variables answers the variables a template names, in the order they
// first come in its subject, HTML and text. None for a template that
// cannot be filled.
func Variables(t store.Template) []Variable {
	var out []Variable
	seen := map[string]bool{}
	for _, p := range parts(t) {
		names, _ := fill.Names(p.text)
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				d, has := t.Defaults[n]
				out = append(out, Variable{Name: n, Default: d, Required: !has})
			}
		}
	}
	return out
}

// check says whether a template may be kept: an *Invalid otherwise.
func (s *Service) check(t store.Template) error {
	if !nameShape.MatchString(t.Name) {
		return &Invalid{"name", `"name" is how a send asks for the template: 1 to 64 of a-z, 0-9, "-" and "_", starting with a letter or a digit.`}
	}
	if strings.TrimSpace(t.Subject) == "" {
		return &Invalid{"subject", `"subject" is required: an email has one.`}
	}
	if len(t.Subject) > maxSubject || strings.ContainsFunc(t.Subject, unicode.IsControl) {
		return &Invalid{"subject", fmt.Sprintf(`"subject" must be at most %d bytes on one line.`, maxSubject)}
	}
	if t.HTML == "" && t.Text == "" {
		return &Invalid{"html", `One of "html" and "text" is required, or both: an email says something.`}
	}
	if n := len(t.Subject) + len(t.HTML) + len(t.Text); s.opt.MaxBytes > 0 && n > s.opt.MaxBytes {
		return &Invalid{"html", fmt.Sprintf(`A template's subject, "html" and "text" may be %d bytes in all; these are %d.`, s.opt.MaxBytes, n)}
	}
	for _, p := range parts(t) {
		var bad *fill.Malformed
		if _, err := fill.Names(p.text); errors.As(err, &bad) {
			return &Invalid{p.field, fmt.Sprintf(`In %q, the "{{" that comes %s is not followed by a variable: write {{name}}, `+
				`where a name is letters, digits and "_" and does not start with a digit. Nothing else is understood between braces.`,
				p.field, ordinal(bad.Nth))}
		}
	}
	named := map[string]bool{}
	for _, v := range Variables(t) {
		named[v.Name] = true
	}
	if len(named) > MaxVariables {
		return &Invalid{"html", fmt.Sprintf(`A template names at most %d variables; this one names %d.`, MaxVariables, len(named))}
	}
	for name := range t.Defaults {
		if !named[name] {
			// The name is the caller's own, and only said when it could
			// name a variable: nothing else is quoted.
			if fill.Name(name) {
				return &Invalid{"defaults", fmt.Sprintf(`"defaults" gives a value for %q, which the template does not name.`, name)}
			}
			return &Invalid{"defaults", `"defaults" gives a value for something the template does not name.`}
		}
	}
	return nil
}

// ordinal says which of several: "1st", "2nd", "3rd", "4th".
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
