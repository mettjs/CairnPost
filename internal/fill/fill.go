// Package fill puts values where a text names variables. A variable is
// a name between double braces, {{name}} or {{ name }}; nothing else is
// understood: no condition, no loop, no call. It knows nothing of email.
package fill

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
)

// MaxName is the longest name a variable may have.
const MaxName = 50

// open is what begins a variable.
const open = "{{"

// variable is a whole variable at the start of what follows an opening.
var variable = regexp.MustCompile(`^\{\{ *([A-Za-z_][A-Za-z0-9_]*) *\}\}`)

// name is a variable's name alone.
var name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Name reports whether s can name a variable.
func Name(s string) bool { return len(s) <= MaxName && name.MatchString(s) }

// Malformed is a text with an opening that names no variable. Nth is
// which opening, from 1: the text itself is not quoted.
type Malformed struct{ Nth int }

func (e *Malformed) Error() string {
	return fmt.Sprintf("fill: opening %d names no variable", e.Nth)
}

// walk hands each stretch of plain text to plain and each variable's
// name to named, in order.
func walk(text string, plain func(string), named func(string)) error {
	for nth := 1; ; nth++ {
		at := strings.Index(text, open)
		if at < 0 {
			plain(text)
			return nil
		}
		plain(text[:at])
		found := variable.FindStringSubmatch(text[at:])
		if found == nil || len(found[1]) > MaxName {
			return &Malformed{Nth: nth}
		}
		named(found[1])
		text = text[at+len(found[0]):]
	}
}

// Names answers the variables a text names, each once, in the order
// they first come; a *Malformed for a text that cannot be filled.
func Names(text string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	err := walk(text, func(string) {}, func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ErrTooLong is a text that, filled, would be longer than was allowed.
var ErrTooLong = errors.New("fill: the filled text is too long")

// Text fills a text with values as they are, to most bytes at most. A
// value is never read for variables of its own. A variable with no
// value is filled with nothing: whoever fills checks Names first.
func Text(text string, values map[string]string, most int) (string, error) {
	return filled(text, values, most, func(v string) string { return v })
}

// HTML fills a text that is HTML: each value is escaped, so it is shown
// as written and is never markup.
func HTML(text string, values map[string]string, most int) (string, error) {
	return filled(text, values, most, html.EscapeString)
}

// filled stops as soon as it is too long: a short text that names one
// long value many times is never written out.
func filled(text string, values map[string]string, most int, as func(string) string) (string, error) {
	var b strings.Builder
	long := false
	write := func(s string) {
		if long = long || b.Len()+len(s) > most; !long {
			b.WriteString(s)
		}
	}
	err := walk(text, write, func(n string) {
		if !long {
			write(as(values[n]))
		}
	})
	if err != nil {
		return "", err
	}
	if long {
		return "", ErrTooLong
	}
	return b.String(), nil
}
