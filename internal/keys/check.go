package keys

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// longestName is the most characters a key's name may have.
const longestName = 100

// domainShape is a host name: labels of letters, digits and hyphens,
// joined by dots, with no port, path or trailing dot.
var domainShape = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

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
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain != "" && (len(domain) > 253 || !domainShape.MatchString(domain)) {
		return "", "", &Invalid{"the sending domain must be a name such as mail.example.com, with no address, port or path"}
	}
	return name, domain, nil
}
