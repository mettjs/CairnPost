package store

import "strings"

// States is every state a message can be in, in the order of an email's
// life. It is the one list of them: what is shown and what is asked for
// by state is checked against it.
var States = []string{StateSending, StateSent, StateDeferred, StateDelivered,
	StateBounced, StateComplained, StateFailed, StateUnknown}

// MessageQuery says which messages to list. A field left empty does not
// narrow the list.
type MessageQuery struct {
	// Domain is the sending domain, folded as DomainOf folds it.
	Domain string
	// Recipient is an address in its wire form. It matches however its
	// letters A to Z were cased when the email was sent; other letters
	// match as they are written.
	Recipient string
	State     string
	// Before is the id of a message: only those older are listed. An id
	// no message could have lists nothing.
	Before string
	// Limit is the most messages answered.
	Limit int
}

// FoldASCII is text with its letters A to Z in lower case and every
// other character as it is: the one fold that is the same in the server
// and in any database, whatever language either sorts in.
func FoldASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}
