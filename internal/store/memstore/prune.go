package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

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
	for id, m := range s.messages {
		if len(gone) < limit && !m.UpdatedAt.After(before) && !keyed[id] {
			gone[id] = true
			delete(s.messages, id)
		}
	}
	// What hangs on a message goes with it; a suppression only lets go.
	s.events = slices.DeleteFunc(s.events, func(e store.Event) bool { return gone[e.MessageID] })
	s.deliveries = slices.DeleteFunc(s.deliveries, func(d *delivery) bool { return gone[d.MessageID] })
	for _, row := range s.suppressed {
		if gone[row.MessageID] {
			row.MessageID = ""
		}
	}
	return len(gone), nil
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
