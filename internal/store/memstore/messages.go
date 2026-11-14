package memstore

import (
	"context"
	"slices"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
)

// Messages implements store.Mail. Ids are made in order, so the greatest
// is the newest, as in the database.
func (s *Store) Messages(_ context.Context, q store.MessageQuery) ([]store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.Message{}
	if q.Before != "" && !strings.HasPrefix(q.Before, "msg-") {
		return out, nil // an id no message could have
	}
	// An address or a state is of one address of an email: the email is
	// listed if one of its addresses fits both.
	fits := map[string]bool{}
	for _, m := range s.messages {
		if (q.Recipient == "" || store.FoldASCII(m.Recipient) == store.FoldASCII(q.Recipient)) && (q.State == "" || m.State == q.State) {
			fits[m.EmailID] = true
		}
	}
	for _, m := range s.messages {
		switch {
		case m.ID != m.EmailID, !fits[m.ID],
			q.Domain != "" && store.DomainOf(m.Sender) != q.Domain,
			q.Before != "" && m.ID >= q.Before:
			continue
		}
		out = append(out, s.email(m))
	}
	slices.SortFunc(out, func(a, b store.Message) int { return strings.Compare(b.ID, a.ID) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}
