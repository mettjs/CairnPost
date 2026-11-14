// Package memstore is a store held in memory, for tests of everything above
// the store. It keeps store/postgres's rules; the integration tests hold
// the two to the same behaviour.
package memstore

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
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

	mu         sync.Mutex
	seq        int
	keys       map[string]store.Key // by prefix
	messages   map[string]store.Message
	idems      map[string]*idem    // by key id + "\x00" + idempotency key
	contents   map[string]content  // by message id
	details    map[string]content  // by event id
	sessions   map[string]session  // by the hash of the token
	previous   map[string][]before // signing keys endpoints had, by endpoint
	events     []store.Event
	suppressed map[string]*suppression
	endpoints  []*store.Endpoint
	deliveries []*delivery
	counts     map[bucket]map[string]int
	held       map[string]*held
}

// New returns an empty store on the wall clock.
func New() *Store {
	return &Store{Now: time.Now, keys: map[string]store.Key{},
		messages: map[string]store.Message{}, idems: map[string]*idem{},
		suppressed: map[string]*suppression{}, contents: map[string]content{}, details: map[string]content{}, sessions: map[string]session{},
		counts: map[bucket]map[string]int{}, held: map[string]*held{}}
}

var _ store.Store = (*Store)(nil)

func (s *Store) next(kind string) string {
	s.seq++
	return fmt.Sprintf("%s-%04d", kind, s.seq)
}

// Ping implements store.Store.
func (*Store) Ping(context.Context) error { return nil }

// CreateKey implements store.Keys.
func (s *Store) CreateKey(_ context.Context, k store.Key, max int) (store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.keys[k.Prefix]; taken {
		return store.Key{}, fmt.Errorf("memstore: prefix %s is taken", k.Prefix)
	}
	live := 0
	for _, had := range s.keys {
		if had.RevokedAt == nil && had.Domain == k.Domain {
			live++
		}
	}
	if max != 0 && live >= max {
		return store.Key{}, store.ErrLimit
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
func (s *Store) ListKeys(_ context.Context, domain string, limit int) ([]store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Key, 0, len(s.keys))
	for _, k := range s.keys {
		if domain == "" || k.Domain == domain {
			out = append(out, k)
		}
	}
	// Newest first; ids are made in order, and settle a tie.
	slices.SortFunc(out, func(a, b store.Key) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	if limit != 0 && len(out) > limit {
		out = out[:limit]
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
	s.cancelOf(k.ID)
	return nil
}

// Message implements store.Mail.
func (s *Store) Message(_ context.Context, id string) (store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[id]
	if !ok || m.EmailID != id {
		return store.Message{}, store.ErrNotFound
	}
	return s.email(m), nil
}

// rows are the ids of an email's rows, a row for each address, in the
// order the addresses were given: ids are made in order.
func (s *Store) rows(email string) []string {
	var ids []string
	for id, m := range s.messages {
		if m.EmailID == email {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// email is an email's first row with every address of it.
func (s *Store) email(m store.Message) store.Message {
	for _, id := range s.rows(m.EmailID) {
		r := s.messages[id]
		m.Recipients = append(m.Recipients, store.Recipient{Address: r.Recipient, Kind: r.Kind, State: r.State, UpdatedAt: r.UpdatedAt})
	}
	return m
}

// newMessage records an email as sending, a row for each address it
// goes to (store.Addressed), and answers its id, those addresses and
// the ones left out; "" when nobody is left.
func (s *Store) newMessage(st store.Start) (string, []store.Addressee, []string) {
	now, email := s.Now(), ""
	kept, dropped := store.Addressed(st.Recipients, func(a store.Addressee) bool {
		return s.inForce(store.SuppressionKey(st.Sender, a.Address))
	})
	if tried := s.rows(st.ID); st.ID != "" && len(tried) > 0 {
		// A try the provider did not take, begun again on its rows: to
		// whom it was to go then, less whoever is suppressed since.
		var was []store.Addressee
		for _, id := range tried {
			was = append(was, store.Addressee{Address: s.messages[id].Recipient, Kind: s.messages[id].Kind})
		}
		kept, dropped = store.Addressed(was, func(a store.Addressee) bool {
			return s.inForce(store.SuppressionKey(st.Sender, a.Address))
		})
		if len(kept) == 0 {
			return "", nil, dropped
		}
		for _, id := range tried {
			m := s.messages[id]
			if i := slices.IndexFunc(kept, func(a store.Addressee) bool { return a.Address == m.Recipient }); i >= 0 {
				s.count(m, m.State, store.StateSending)
				m.State, m.Kind, m.UpdatedAt = store.StateSending, kept[i].Kind, now
			}
			if id == st.ID {
				m.LeftOut = append(m.LeftOut, dropped...)
				dropped = m.LeftOut
			}
			s.messages[id] = m
		}
		return st.ID, kept, dropped
	}
	for _, r := range kept {
		id := s.next("msg")
		if email == "" && st.ID != "" {
			id = st.ID
		}
		m := store.Message{ID: id, KeyID: st.KeyID, Sender: st.Sender, Recipient: r.Address, Kind: r.Kind,
			Driver: st.Driver, State: store.StateSending, CreatedAt: now, UpdatedAt: now}
		if email == "" {
			email, m.LeftOut = m.ID, dropped
		}
		m.EmailID = email
		s.messages[m.ID] = m
		s.count(m, "", m.State)
	}
	if email != "" && st.Content != nil {
		s.contents[email] = content{raw: bytes.Clone(st.Content()), sent: now, expires: now.Add(st.KeepContent)}
	}
	return email, kept, dropped
}

// content is words kept: an email's, or a receiving server's about it.
// sent is when the email was, and expires when the words are to go.
type content struct {
	raw           []byte
	sent, expires time.Time
}

// shown says whether words are answered to one who keeps for keep, and
// until when.
func (c content) shown(now time.Time, keep time.Duration) (time.Time, bool) {
	until := c.sent.Add(keep)
	if c.expires.Before(until) {
		until = c.expires
	}
	return until, now.Before(until)
}

// Content implements store.Mail.
func (s *Store) Content(_ context.Context, id string, keep time.Duration) ([]byte, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, kept := s.contents[id]
	until, shown := c.shown(s.Now(), keep)
	if !kept || !shown {
		return nil, time.Time{}, nil
	}
	return bytes.Clone(c.raw), until, nil
}

// setState moves one address's row.
func (s *Store) setState(id, state, upstreamID string) {
	m, kept := s.messages[id]
	if !kept {
		return // as an UPDATE that finds no row: nothing is made of it
	}
	s.count(m, m.State, state)
	m.State, m.UpdatedAt = state, s.Now()
	if upstreamID != "" {
		m.UpstreamID = upstreamID
	}
	s.messages[id] = m
}

// settle moves every address of an email: what the upstream answered a
// send is the same for all of them. only, if set, is the state a row
// must be in to be moved.
func (s *Store) settle(email, state, upstreamID, only string) {
	for _, id := range s.rows(email) {
		if only == "" || s.messages[id].State == only {
			s.setState(id, state, upstreamID)
		}
	}
}

// Start implements store.Mail.
func (s *Store) Start(_ context.Context, st store.Start) (store.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only a send that would start is refused: a request already sent is
	// still replayed.
	if st.IdemKey == "" {
		return s.start(st), nil
	}
	slot, now := st.KeyID+"\x00"+st.IdemKey, s.Now()
	row, held := s.idems[slot]
	expired := held && now.Sub(row.created) >= st.TTL
	inDoubt := held && !row.done && now.Sub(row.tried) >= st.InDoubt
	if st.ReplayOnly && (!held || expired || !row.done || !bytes.Equal(row.fingerprint, st.Fingerprint)) {
		return store.Claim{Outcome: store.Absent}, nil
	}
	switch {
	case !held, expired, inDoubt:
		// Nobody holds the key, it was remembered long enough, or its last
		// try's answer never came: this request sends, and the key's life
		// starts again from now.
		old := ""
		if held {
			s.settle(row.messageID, store.StateUnknown, "", store.StateSending)
		}
		if held && !row.done {
			old = row.messageID
		}
		c := s.start(st)
		switch c.Outcome {
		case store.Suppressed:
			delete(s.idems, slot)
			c.ID = old
			return c, nil
		case store.Full:
			return c, nil
		}
		s.idems[slot] = &idem{fingerprint: st.Fingerprint, messageID: c.ID, done: st.Hold != nil, created: now, tried: now}
		return c, nil
	case row.done && !bytes.Equal(row.fingerprint, st.Fingerprint):
		return store.Claim{Outcome: store.Mismatch}, nil
	case row.done:
		// Who was left out the first time is said again.
		return store.Claim{Outcome: store.Replay, ID: row.messageID, Dropped: s.messages[row.messageID].LeftOut}, nil
	}
	return store.Claim{Outcome: store.InFlight, RetryAfter: row.tried.Add(st.InDoubt).Sub(now)}, nil
}

// start records an email as sending, unless every address of it is
// suppressed.
func (s *Store) start(st store.Start) store.Claim {
	if st.Hold != nil {
		return s.hold(st)
	}
	if st.ID != "" {
		// An email that waited: begun only by the one who took it, and
		// only while nothing of it was handed to the upstream.
		if !s.begun(st.ID, st.Claim) {
			return store.Claim{Outcome: store.Gone}
		}
		for _, row := range s.rows(st.ID) {
			if s.messages[row].State != store.StateFailed {
				return store.Claim{Outcome: store.Gone}
			}
		}
	}
	id, kept, dropped := s.newMessage(st)
	if id == "" {
		return store.Claim{Outcome: store.Suppressed}
	}
	return store.Claim{Outcome: store.Started, ID: id, Recipients: kept, Dropped: dropped}
}

// Lost implements store.Mail.
func (s *Store) Lost(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle(id, store.StateUnknown, "", store.StateSending)
	s.settled(id)
	return nil
}

// Sent implements store.Mail.
func (s *Store) Sent(_ context.Context, id, upstreamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An address still failed was left out of the try that was taken.
	for _, row := range s.rows(id) {
		if s.messages[row].State != store.StateFailed {
			s.setState(row, store.StateSent, upstreamID)
		}
	}
	for _, row := range s.idems {
		if row.messageID == id {
			row.done = true
		}
	}
	s.settled(id)
	return nil
}

// Unsent implements store.Mail.
func (s *Store) Unsent(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle(id, store.StateFailed, "", "")
	return nil
}

// Failed implements store.Mail.
func (s *Store) Failed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle(id, store.StateFailed, "", "")
	for at, row := range s.idems {
		if row.messageID == id {
			delete(s.idems, at)
		}
	}
	s.settled(id)
	return nil
}
