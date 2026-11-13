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
	for _, m := range s.messages {
		switch {
		case q.Domain != "" && store.DomainOf(m.Sender) != q.Domain,
			q.Recipient != "" && store.FoldASCII(m.Recipient) != store.FoldASCII(q.Recipient),
			q.State != "" && m.State != q.State,
			q.Before != "" && m.ID >= q.Before:
			continue
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b store.Message) int { return strings.Compare(b.ID, a.ID) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}
