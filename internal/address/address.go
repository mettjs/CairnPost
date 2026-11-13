// Package address is the one form an email address is kept and compared
// in, so that the paths that write an address and the paths that look one
// up cannot disagree.
package address

import (
	netmail "net/mail"
	"strings"
)

// Wire is an address alone, as it is written on the wire: a local part
// that needs quotes keeps them. It is the form an address is handed to a
// driver and kept in.
func Wire(a *netmail.Address) string {
	s := (&netmail.Address{Address: a.Address}).String()
	return strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
}

// Bare reads text that is an address and nothing else, with no name
// beside it, and answers it as Wire does; false for anything else.
func Bare(text string) (string, bool) {
	a, err := netmail.ParseAddress(strings.TrimSpace(text))
	if err != nil || a.Name != "" {
		return "", false
	}
	return Wire(a), true
}
