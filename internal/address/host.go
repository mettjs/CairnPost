package address

import (
	"regexp"
	"strings"
)

// hostShape is a host name: labels of letters, digits and hyphens joined
// by dots, at least two of them, with no port, path or trailing dot.
var hostShape = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Host answers a domain as it is kept, in lower case with no space
// around it, and whether it is a host name: the one rule for what a
// sending domain can be, for whoever binds a key to one and whoever
// looks one up.
func Host(domain string) (string, bool) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return domain, len(domain) <= 253 && hostShape.MatchString(domain)
}
