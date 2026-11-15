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

// ending finds an email that waits and is not taken (claim 0), or one
// taken under a claim, and changes it.
func (s *Store) ending(id string, claim int, change func(*held)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.held[id]
	switch {
	case !ok:
		return store.ErrNotFound
	case h.State == store.StateCancelled || h.taken != (claim > 0) || claim > 0 && h.Claim != claim:
		return store.ErrTooLate
	}
	change(h)
	h.UpdatedAt = s.Now()
	return nil
}

// cancel ends an email's wait and forgets what it said, with what a
// failed try of it kept of its words.
func (s *Store) cancel(h *held, reason string) {
	h.State, h.Reason, h.taken, h.words, h.files, h.bytes = store.StateCancelled, reason, false, nil, nil, 0
	delete(s.contents, h.ID)
}

// Cancel implements store.Schedule.
func (s *Store) Cancel(_ context.Context, id string) error {
	return s.ending(id, 0, func(h *held) { s.cancel(h, "") })
}

// Drop implements store.Schedule.
func (s *Store) Drop(_ context.Context, id string, claim int, reason string) error {
	return s.ending(id, claim, func(h *held) { s.cancel(h, reason) })
}

// Reschedule implements store.Schedule.
func (s *Store) Reschedule(_ context.Context, id string, at time.Time) error {
	return s.ending(id, 0, func(h *held) { h.SendAt, h.next, h.Tries = at, at, 0 })
}

// Postpone implements store.Schedule.
func (s *Store) Postpone(_ context.Context, id string, claim int, wait time.Duration) error {
	return s.ending(id, claim, func(h *held) {
		h.taken, h.next = false, s.Now().Add(wait)
		s.settle(id, store.StateFailed, "", store.StateSending)
	})
}

// Release implements store.Schedule.
func (s *Store) Release(_ context.Context, id string, claim int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.held[id]; ok && h.taken && h.Claim == claim {
		delete(s.held, id)
	}
	return nil
}

// settled ends the wait of an email whose message was settled for good.
func (s *Store) settled(id string) {
	if h, ok := s.held[id]; ok && h.taken {
		delete(s.held, id)
	}
}

// begun marks a taken email as begun under its taking: false when it is
// no longer that taker's.
func (s *Store) begun(id string, claim int) bool {
	h, ok := s.held[id]
	if !ok || !h.taken || h.Claim != claim {
		return false
	}
	h.claimed, h.Tries = s.Now(), h.Tries+1
	return true
}

// cancelOf cancels the waiting emails of a key that was revoked.
func (s *Store) cancelOf(keyID string) {
	for _, h := range s.held {
		if h.KeyID == keyID && h.State == store.StateScheduled && !h.taken {
			s.cancel(h, store.HeldKeyRevoked)
			h.UpdatedAt = s.Now()
		}
	}
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
			s.cancel(h, store.HeldKeyRevoked)
			continue
		}
		h.taken, h.claimed, h.Claim = true, now, h.Claim+1
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
