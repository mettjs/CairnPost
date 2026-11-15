package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// PruneContents implements store.Pruning.
func (s *Store) PruneContents(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return expire(s.contents, s.Now(), limit), nil
}

// PruneDetails implements store.Pruning.
func (s *Store) PruneDetails(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return expire(s.details, s.Now(), limit), nil
}

// expire deletes up to limit of the words whose time is up.
func expire(kept map[string]content, now time.Time, limit int) int {
	n := 0
	for id, c := range kept {
		if n < limit && !c.expires.After(now) {
			delete(kept, id)
			n++
		}
	}
	return n
}

// PruneKeys implements store.Pruning.
func (s *Store) PruneKeys(_ context.Context, ttl time.Duration, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, now := 0, s.Now()
	for at, row := range s.idems {
		if n == limit {
			break
		}
		if now.Sub(row.created) < ttl {
			continue
		}
		if !row.done && s.messages[row.messageID].State == store.StateSending {
			s.setState(row.messageID, store.StateUnknown, "")
		}
		delete(s.idems, at)
		n++
	}
	return n, nil
}

// PruneNotices implements store.Pruning.
func (s *Store) PruneNotices(_ context.Context, before time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	s.deliveries = slices.DeleteFunc(s.deliveries, func(d *delivery) bool {
		if n == limit || d.about == nil || d.CreatedAt.After(before) {
			return false
		}
		n++
		return true
	})
	return n, nil
}

// ForgetSecrets implements store.Pruning.
func (s *Store) ForgetSecrets(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, keys := range s.previous {
		s.previous[id] = slices.DeleteFunc(keys, func(k before) bool {
			if n == limit || k.until.After(s.Now()) {
				return false
			}
			n++
			return true
		})
	}
	return n, nil
}

// MarkUnanswered implements store.Pruning.
func (s *Store) MarkUnanswered(_ context.Context, older time.Duration, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, now := 0, s.Now()
	for id, m := range s.messages {
		if n < limit && m.State == store.StateSending && now.Sub(m.UpdatedAt) >= older {
			// Its record stays kept from when it was sent.
			s.count(m, m.State, store.StateUnknown)
			m.State = store.StateUnknown
			s.messages[id] = m
			n++
		}
	}
	return n, nil
}

// Horizon implements store.Pruning.
func (s *Store) Horizon(_ context.Context, keep time.Duration) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Now().Add(-keep), nil
}

// PruneMessages implements store.Pruning.
func (s *Store) PruneMessages(_ context.Context, before time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keyed, gone := map[string]bool{}, map[string]bool{}
	for _, row := range s.idems {
		keyed[row.messageID] = true
	}
	// An email goes whole, once nothing has come of any address of it
	// since. limit is of emails.
	emails := 0
	for id, m := range s.messages {
		if emails == limit || m.EmailID != id || keyed[id] {
			continue
		}
		rows := s.rows(id)
		if slices.ContainsFunc(rows, func(row string) bool { return s.messages[row].UpdatedAt.After(before) }) {
			continue
		}
		emails++
		for _, row := range rows {
			gone[row] = true
			delete(s.messages, row)
		}
	}
	// What hangs on a message goes with it; a suppression only lets go.
	for id := range gone {
		delete(s.contents, id)
	}
	for _, e := range s.events {
		if gone[e.MessageID] {
			delete(s.details, e.ID)
		}
	}
	s.events = slices.DeleteFunc(s.events, func(e store.Event) bool { return gone[e.MessageID] })
	s.deliveries = slices.DeleteFunc(s.deliveries, func(d *delivery) bool { return gone[d.MessageID] })
	for _, row := range s.suppressed {
		if gone[row.MessageID] {
			row.MessageID = ""
		}
	}
	return emails, nil
}

// PruneLifted implements store.Pruning.
func (s *Store) PruneLifted(_ context.Context, before time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The oldest message kept: a lift made at or after it is held back.
	var oldest time.Time
	for _, m := range s.messages {
		if oldest.IsZero() || m.CreatedAt.Before(oldest) {
			oldest = m.CreatedAt
		}
	}
	n := 0
	for at, row := range s.suppressed {
		held := !oldest.IsZero() && !oldest.After(*orNow(row.lifted))
		if n < limit && row.lifted != nil && !row.lifted.After(before) && !held {
			delete(s.suppressed, at)
			// What told of it on its own goes with it.
			s.deliveries = slices.DeleteFunc(s.deliveries, func(d *delivery) bool {
				return d.about != nil && d.about.Domain == row.Domain && d.about.Address == row.Address
			})
			n++
		}
	}
	return n, nil
}

// orNow is a lift's time, or a time nothing is before when there is none.
func orNow(lifted *time.Time) *time.Time {
	if lifted == nil {
		return &time.Time{}
	}
	return lifted
}
