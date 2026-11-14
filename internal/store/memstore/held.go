package memstore

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// held is an email that waits, with what it says.
type held struct {
	store.Held
	taken   bool // by a sender, for a try
	claimed time.Time
	next    time.Time
	words   []byte
	files   []store.HeldFile
	bytes   int64
}

// hold keeps an email to be sent later.
func (s *Store) hold(st store.Start) store.Claim {
	size := int64(len(st.Hold.Words))
	for _, f := range st.Hold.Files {
		size += int64(len(f.Content))
	}
	if st.Hold.Room > 0 {
		var sum int64
		for _, h := range s.held {
			sum += h.bytes
		}
		if sum+size > st.Hold.Room {
			return store.Claim{Outcome: store.Full}
		}
	}
	now := s.Now()
	h := &held{Held: store.Held{ID: s.next("held"), KeyID: st.KeyID, Sender: st.Sender, Recipients: slices.Clone(st.Recipients),
		SendAt: st.Hold.SendAt, State: store.StateScheduled, CreatedAt: now, UpdatedAt: now},
		next: st.Hold.SendAt, words: bytes.Clone(st.Hold.Words), files: slices.Clone(st.Hold.Files), bytes: size}
	s.held[h.ID] = h
	return store.Claim{Outcome: store.Started, ID: h.ID}
}

// Held implements store.Schedule.
func (s *Store) Held(_ context.Context, id string) (store.Held, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.held[id]
	if !ok {
		return store.Held{}, store.ErrNotFound
	}
	return h.Held, nil
}

// HeldWords implements store.Schedule.
func (s *Store) HeldWords(_ context.Context, id string) ([]byte, []store.HeldFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.held[id]
	if !ok || h.words == nil {
		return nil, nil, nil
	}
	return bytes.Clone(h.words), slices.Clone(h.files), nil
}

// waiting are the emails that wait, of a domain or of all, soonest first.
func (s *Store) waiting(domain string) []store.Held {
	var out []store.Held
	for _, h := range s.held {
		if h.State != store.StateCancelled && (domain == "" || store.DomainOf(h.Sender) == domain) {
			out = append(out, h.Held)
		}
	}
	slices.SortFunc(out, func(a, b store.Held) int {
		return cmp.Or(a.SendAt.Compare(b.SendAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// ListHeld implements store.Schedule.
func (s *Store) ListHeld(_ context.Context, domain string, after time.Time, limit int) ([]store.Held, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Held
	for _, h := range s.waiting(domain) {
		if h.SendAt.After(after) && len(out) < limit {
			out = append(out, h)
		}
	}
	return out, nil
}

// Waiting implements store.Schedule.
func (s *Store) Waiting(_ context.Context, domain string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waiting(domain)), nil
}

// ending finds an email that is waiting (taken or not, as asked) and
// changes it.
func (s *Store) ending(id string, taken bool, change func(*held)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.held[id]
	switch {
	case !ok:
		return store.ErrNotFound
	case h.State == store.StateCancelled || h.taken != taken:
		return store.ErrTooLate
	}
	change(h)
	h.UpdatedAt = s.Now()
	return nil
}

func cancel(h *held, reason string) {
	h.State, h.Reason, h.taken, h.words, h.files, h.bytes = store.StateCancelled, reason, false, nil, nil, 0
}

// Cancel implements store.Schedule.
func (s *Store) Cancel(_ context.Context, id string) error {
	return s.ending(id, false, func(h *held) { cancel(h, "") })
}

// Drop implements store.Schedule.
func (s *Store) Drop(_ context.Context, id, reason string) error {
	return s.ending(id, true, func(h *held) { cancel(h, reason) })
}

// Reschedule implements store.Schedule.
func (s *Store) Reschedule(_ context.Context, id string, at time.Time) error {
	return s.ending(id, false, func(h *held) { h.SendAt, h.next = at, at })
}

// Postpone implements store.Schedule.
func (s *Store) Postpone(_ context.Context, id string, wait time.Duration) error {
	return s.ending(id, true, func(h *held) { h.taken, h.next = false, s.Now().Add(wait) })
}

// Release implements store.Schedule.
func (s *Store) Release(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.held[id]; ok && h.taken {
		delete(s.held, id)
	}
	return nil
}

// revoked says whether a key, by its id, was revoked.
func (s *Store) revoked(keyID string) bool {
	for _, k := range s.keys {
		if k.ID == keyID {
			return k.RevokedAt != nil
		}
	}
	return false
}

// TakeDue implements store.Schedule.
func (s *Store) TakeDue(_ context.Context, limit int) ([]store.Held, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	var due []*held
	for _, h := range s.held {
		if h.State == store.StateScheduled && !h.taken && !h.next.After(now) {
			due = append(due, h)
		}
	}
	slices.SortFunc(due, func(a, b *held) int { return cmp.Or(a.next.Compare(b.next), cmp.Compare(a.ID, b.ID)) })
	var out []store.Held
	for _, h := range due[:min(limit, len(due))] {
		if h.UpdatedAt = now; s.revoked(h.KeyID) {
			cancel(h, store.HeldKeyRevoked)
			continue
		}
		h.taken, h.claimed, h.Tries = true, now, h.Tries+1
		out = append(out, h.Held)
	}
	return out, nil
}

// Recover implements store.Schedule.
func (s *Store) Recover(_ context.Context, older time.Duration, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, now := 0, s.Now()
	for id, h := range s.held {
		if !h.taken || now.Sub(h.claimed) < older || n >= limit {
			continue
		}
		n++
		handed := false
		for _, row := range s.rows(id) {
			handed = handed || s.messages[row].State != store.StateFailed
		}
		if handed {
			delete(s.held, id)
			continue
		}
		h.taken, h.next, h.UpdatedAt = false, now, now
	}
	return n, nil
}

// PruneCancelled implements store.Pruning.
func (s *Store) PruneCancelled(_ context.Context, before time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, h := range s.held {
		if h.State == store.StateCancelled && !h.UpdatedAt.After(before) && n < limit {
			delete(s.held, id)
			n++
		}
	}
	return n, nil
}
