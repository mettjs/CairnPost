package mail

import (
	"context"
	netmail "net/mail"
	"slices"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/store"
)

// MayRead says whether a key may read a message's record. A key reads
// the emails it sent. An admin key reads any, unless it is bound to a
// sending domain: then that domain's only, as with everything else it
// manages.
func MayRead(key store.Key, m store.Message) bool {
	if m.KeyID == key.ID {
		return true
	}
	return key.Scope == store.ScopeAdmin && (key.Domain == "" || key.Domain == store.DomainOf(m.Sender))
}

// ReadsContent says whether a key is shown what the emails it may read
// said, where that is kept: whoever runs the server is, an application
// that only sends is not, even for its own emails.
func ReadsContent(key store.Key) bool { return key.Scope == store.ScopeAdmin }

// MessageFor answers, for a key, a message's record and what happened to
// it since, oldest first, as they stood at one moment. store.ErrNotFound
// for none, and for one the key may not read, which is not told apart.
func (s *Service) MessageFor(ctx context.Context, key store.Key, id string) (store.Message, []store.Event, error) {
	m, events, err := s.events.Record(ctx, id)
	if err != nil {
		return store.Message{}, nil, err
	}
	if !MayRead(key, m) {
		return store.Message{}, nil, store.ErrNotFound
	}
	return m, events, nil
}

// Recipient reads an address someone typed, with or without a name
// beside it, as the address an email to it is kept under. Its error is
// an *Invalid.
func Recipient(typed string) (string, error) {
	to, err := netmail.ParseAddress(typed)
	if err != nil || len(address.Wire(to)) > maxAddress {
		return "", &Invalid{"to", `Give one whole address, such as "ana@example.org".`}
	}
	return address.Wire(to), nil
}

// Query says which emails to list. A field left empty does not narrow
// the list.
type Query struct {
	// To is an address as someone typed it, with or without a name.
	To    string
	State string
	// Before is the id of an email: only those older are listed.
	Before string
	Limit  int
}

// mostListed is the most emails one call lists.
const mostListed = 200

// List is the emails an admin key may read, newest first; none for a key
// that may only send. Its error for a query that cannot be asked is an
// *Invalid.
func (s *Service) List(ctx context.Context, key store.Key, q Query) ([]store.Message, error) {
	if key.Scope != store.ScopeAdmin {
		return nil, nil
	}
	ask := store.MessageQuery{Domain: key.Domain, State: q.State, Before: q.Before, Limit: min(max(q.Limit, 1), mostListed)}
	if q.To != "" {
		to, err := Recipient(q.To)
		if err != nil {
			return nil, err
		}
		ask.Recipient = to
	}
	if q.State != "" && !slices.Contains(store.States, q.State) {
		return nil, &Invalid{"state", "There is no such state."}
	}
	return s.store.Messages(ctx, ask)
}
