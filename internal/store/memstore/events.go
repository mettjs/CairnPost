package memstore

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// RecordEvent implements store.Events.
func (s *Store) RecordEvent(_ context.Context, e store.Event) (store.Recorded, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.about(e)
	reason, suppresses := store.SuppressionFor(e.Kind, e.Permanent, e.Code)
	// A lifted suppression comes back only for an email sent after the lift.
	since := func(sent time.Time) func(time.Time) bool {
		return func(lifted time.Time) bool { return lifted.Before(sent) }
	}
	if m.ID == "" {
		if suppresses && e.Sender != "" && e.Recipient != "" {
			domain, address := store.SuppressionKey(e.Sender, e.Recipient)
			// When the email was sent is not known: a lift is never undone.
			sup := store.Suppression{Domain: domain, Address: address, Reason: reason}
			if s.suppress(sup, func(time.Time) bool { return false }) {
				// No event of an email will tell of it: it is told on its own.
				s.oweNotice(s.suppressed[slot(domain, address)].Suppression)
			}
		}
		return store.NoMessage, nil
	}
	for _, had := range s.events {
		if had.Driver == e.Driver && had.UpstreamEventID == e.UpstreamEventID {
			return store.Duplicate, nil
		}
	}
	newer := true
	for _, had := range s.events {
		if had.MessageID == m.ID && had.Kind == m.State && !had.At.Before(e.At) {
			newer = false
		}
	}
	e.ID, e.MessageID = s.next("evt"), m.ID
	e.Sender, e.Recipient = "", "" // never kept with the event
	if e.Detail != "" {
		// Kept apart, so nothing that reads the event reads it.
		if d := (content{raw: []byte(e.Detail), sent: m.CreatedAt, expires: m.CreatedAt.Add(e.KeepDetail)}); d.expires.After(s.Now()) {
			s.details[e.ID] = d
		}
		e.Detail, e.KeepDetail = "", 0
	}
	if suppresses {
		domain, address := store.SuppressionKey(m.Sender, m.Recipient)
		// A suppression names the email, not the row of one address.
		s.suppress(store.Suppression{Domain: domain, Address: address, Reason: reason, MessageID: m.EmailID}, since(m.CreatedAt))
		// One in force by an event that could name no message is given this one.
		if row := s.suppressed[slot(domain, address)]; row.lifted == nil && row.Reason == reason && row.MessageID == "" {
			row.MessageID = m.EmailID
		}
		// Whether the address is on the list now is kept with the event.
		e.Suppressed = s.inForce(domain, address)
	}
	s.events = append(s.events, e)
	s.owe(e, store.DomainOf(m.Sender))
	// Every event kept is something that happened to the email.
	to := m.State
	if store.Moves(m.State, e.Kind, newer) {
		to = store.StateOf[e.Kind]
	}
	s.setState(m.ID, to, "")
	// An email that went to nobody is free to be tried again under its
	// key; one that failed at some addresses is not: it would go twice
	// to the others.
	if !slices.ContainsFunc(s.rows(m.EmailID), func(id string) bool { return s.messages[id].State != store.StateFailed }) {
		for at, row := range s.idems {
			if row.messageID == m.EmailID {
				delete(s.idems, at)
			}
		}
	}
	return store.Kept, nil
}

// Record implements store.Events.
func (s *Store) Record(ctx context.Context, messageID string) (store.Message, []store.Event, error) {
	m, err := s.Message(ctx, messageID)
	if err != nil {
		return store.Message{}, nil, err
	}
	events, err := s.EventsOf(ctx, messageID)
	return m, events, err
}

// Details implements store.Events.
func (s *Store) Details(_ context.Context, messageID string, keep time.Duration) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, e := range s.events {
		d, kept := s.details[e.ID]
		if _, shown := d.shown(s.Now(), keep); kept && shown && s.messages[e.MessageID].EmailID == messageID {
			out[e.ID] = string(d.raw)
		}
	}
	return out, nil
}

// about is the row an event is about: of the address it names, or the
// only one of an email that went to one address. None ("" id) for an
// address the email did not go to, and for an event that names none
// about an email to several: there is no telling whose it is.
func (s *Store) about(e store.Event) store.Message {
	var first store.Message
	if e.UpstreamID == "" {
		return first
	}
	for _, m := range s.messages {
		if m.Driver != e.Driver || m.UpstreamID != e.UpstreamID {
			continue
		}
		if e.Recipient != "" && store.FoldASCII(m.Recipient) == store.FoldASCII(e.Recipient) {
			return m
		}
		if m.ID == m.EmailID {
			first = m
		}
	}
	if first.ID != "" && len(s.rows(first.EmailID)) > 1 {
		return store.Message{}
	}
	return first
}

// EventsOf implements store.Events.
func (s *Store) EventsOf(_ context.Context, messageID string) ([]store.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Event
	for _, e := range s.events {
		if m := s.messages[e.MessageID]; m.EmailID == messageID {
			e.Recipient = m.Recipient
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// EventsAfter implements store.Events: events are kept in the order
// they were recorded, and none comes into view late, so there is
// nothing to look back for.
func (s *Store) EventsAfter(_ context.Context, after string, _ time.Duration, domain string, limit int) ([]store.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Event
	seen := after == ""
	for _, e := range s.events {
		m := s.messages[e.MessageID]
		if seen && (domain == "" || store.DomainOf(m.Sender) == domain) {
			e.Sender, e.Recipient, e.MessageID = m.Sender, m.Recipient, m.EmailID
			out = append(out, e)
		}
		if e.ID == after {
			seen = true
		}
	}
	switch {
	case after == "" && len(out) > limit:
		out = out[len(out)-limit:]
	case after != "" && len(out) > limit:
		out = out[:limit]
	}
	return out, nil
}
