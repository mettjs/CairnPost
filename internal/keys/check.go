package keys

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mettjs/cairnpost/internal/address"
)

// longestName is the most characters a key's name may have.
const longestName = 100

// checked answers a key's name and sending domain as they are kept, or
// an *Invalid saying what to fix. The name is shown in lists, in a
// terminal among them, so it holds nothing that moves a cursor or breaks
// a line.
func checked(name, domain string) (string, string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", "", &Invalid{"a key needs a name saying what uses it"}
	case !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || !unicode.IsPrint(r) }):
		return "", "", &Invalid{"the name must be plain text on one line"}
	case utf8.RuneCountInString(name) > longestName:
		return "", "", &Invalid{"the name must be at most 100 characters"}
	}
	domain, isHost := address.Host(domain)
	if domain != "" && !isHost {
		return "", "", &Invalid{"the sending domain must be a name such as mail.example.com, with no address, port or path"}
	}
	return name, domain, nil
}
