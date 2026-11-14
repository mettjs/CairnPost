package store

import "time"

// Addressed decides who an email goes to once the suppressed are left out,
// which both stores decide alike. The addresses left out are answered
// beside those kept.
//
// An email is to someone: left with no "to", its first "cc" takes that
// place, since a copy is addressed in the open as "to" is. A "bcc"
// never does: it would be shown to the other hidden ones. So an email
// left with nobody in the open goes to nobody, and kept is nil.
func Addressed(asked []Addressee, suppressed func(Addressee) bool) (kept []Addressee, dropped []string) {
	for _, a := range asked {
		if suppressed(a) {
			dropped = append(dropped, a.Address)
			continue
		}
		kept = append(kept, a)
	}
	// They are asked in the order "to", "cc", "bcc", so the first one
	// kept says who is left in the open.
	if len(kept) == 0 || kept[0].Kind == KindBcc {
		return nil, dropped
	}
	kept[0].Kind = KindTo
	return kept, dropped
}

// Heard is when anything last came of the email at any of its addresses.
func (m Message) Heard() time.Time {
	at := m.UpdatedAt
	for _, r := range m.Recipients {
		if r.UpdatedAt.After(at) {
			at = r.UpdatedAt
		}
	}
	return at
}
