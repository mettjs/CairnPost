// Package memstore is a store held in memory, for tests of everything above
// the store. It keeps store/postgres's rules; the integration tests hold
// the two to the same behaviour.
package memstore

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

type idem struct {
	fingerprint []byte
	messageID   string
	done        bool
	created     time.Time
	tried       time.Time
}

// Store is the in-memory store. Now is its clock.
type Store struct {
	Now func() time.Time

	mu       sync.Mutex
	seq      int
	keys     map[string]store.Key // by prefix
	messages map[string]store.Message
	idems    map[string]*idem // by key id + "\x00" + idempotency key
}

// New returns an empty store on the wall clock.
func New() *Store {
	return &Store{Now: time.Now, keys: map[string]store.Key{},
		messages: map[string]store.Message{}, idems: map[string]*idem{}}
}

var _ store.Store = (*Store)(nil)

func (s *Store) next(kind string) string {
	s.seq++
	return fmt.Sprintf("%s-%04d", kind, s.seq)
}

// Ping implements store.Store.
func (*Store) Ping(context.Context) error { return nil }

// CreateKey implements store.Keys.
func (s *Store) CreateKey(_ context.Context, k store.Key) (store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.keys[k.Prefix]; taken {
		return store.Key{}, fmt.Errorf("memstore: prefix %s is taken", k.Prefix)
	}
	k.ID, k.CreatedAt = s.next("key"), s.Now()
	s.keys[k.Prefix] = k
	return k, nil
}

// KeyByPrefix implements store.Keys.
func (s *Store) KeyByPrefix(_ context.Context, prefix string) (store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[prefix]
	if !ok {
		return store.Key{}, store.ErrNotFound
	}
	return k, nil
}

// ListKeys implements store.Keys.
func (s *Store) ListKeys(context.Context) ([]store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Key, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k)
	}
	return out, nil
}

// RevokeKey implements store.Keys.
func (s *Store) RevokeKey(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[prefix]
	if !ok || k.RevokedAt != nil {
		return store.ErrNotFound
	}
	now := s.Now()
	k.RevokedAt = &now
	s.keys[prefix] = k
	return nil
}

// Message implements store.Mail.
func (s *Store) Message(_ context.Context, id string) (store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[id]
	if !ok {
		return store.Message{}, store.ErrNotFound
	}
	return m, nil
}

func (s *Store) newMessage(st store.Start) string {
	now := s.Now()
	m := store.Message{ID: s.next("msg"), KeyID: st.KeyID, Sender: st.Sender, Recipient: st.Recipient,
		Driver: st.Driver, State: store.StateSending, CreatedAt: now, UpdatedAt: now}
	s.messages[m.ID] = m
	return m.ID
}

func (s *Store) setState(id, state, upstreamID string) {
	m := s.messages[id]
	m.State, m.UpdatedAt = state, s.Now()
	if upstreamID != "" {
		m.UpstreamID = upstreamID
	}
	s.messages[id] = m
}

// Start implements store.Mail.
func (s *Store) Start(_ context.Context, st store.Start) (store.Outcome, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.IdemKey == "" {
		return store.Started, s.newMessage(st), nil
	}
	at, now := st.KeyID+"\x00"+st.IdemKey, s.Now()
	row, ok := s.idems[at]
	if ok && now.Sub(row.created) >= st.TTL {
		ok = false // remembered long enough: the key is new again
	}
	switch {
	case !ok:
		id := s.newMessage(st)
		s.idems[at] = &idem{fingerprint: st.Fingerprint, messageID: id, created: now, tried: now}
		return store.Started, id, nil
	case !bytes.Equal(row.fingerprint, st.Fingerprint):
		return store.Mismatch, "", nil
	case row.done:
		return store.Replay, row.messageID, nil
	case now.Sub(row.tried) < st.InDoubt:
		return store.InFlight, "", nil
	}
	// The last try's answer never came: its message's fate is unknown, and
	// this request sends again.
	s.setState(row.messageID, store.StateUnknown, "")
	row.messageID, row.tried = s.newMessage(st), now
	return store.Started, row.messageID, nil
}

// Sent implements store.Mail.
func (s *Store) Sent(_ context.Context, id, upstreamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setState(id, store.StateSent, upstreamID)
	for _, row := range s.idems {
		if row.messageID == id {
			row.done = true
		}
	}
	return nil
}

// Failed implements store.Mail.
func (s *Store) Failed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setState(id, store.StateFailed, "")
	for at, row := range s.idems {
		if row.messageID == id {
			delete(s.idems, at)
		}
	}
	return nil
}
