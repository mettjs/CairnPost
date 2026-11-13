package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// RecordEvent implements store.Events.
func (s *Store) RecordEvent(_ context.Context, e store.Event) (store.Recorded, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m store.Message
	for _, candidate := range s.messages {
		if candidate.Driver == e.Driver && candidate.UpstreamID == e.UpstreamID && e.UpstreamID != "" {
			m = candidate
		}
	}
	reason, suppresses := store.SuppressionFor(e.Kind, e.Permanent, e.Code)
	// A lifted suppression comes back only for an email sent after the lift.
	since := func(sent time.Time) func(time.Time) bool {
		return func(lifted time.Time) bool { return lifted.Before(sent) }
	}
	if m.ID == "" {
		if suppresses && e.Sender != "" && e.Recipient != "" {
			domain, address := store.SuppressionKey(e.Sender, e.Recipient)
			// When the email was sent is not known: a lift is never undone.
			s.suppress(store.Suppression{Domain: domain, Address: address, Reason: reason}, func(time.Time) bool { return false })
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
	if suppresses {
		domain, address := store.SuppressionKey(m.Sender, m.Recipient)
		s.suppress(store.Suppression{Domain: domain, Address: address, Reason: reason, MessageID: m.ID}, since(m.CreatedAt))
		// One in force by an event that could name no message is given this one.
		if row := s.suppressed[slot(domain, address)]; row.lifted == nil && row.Reason == reason && row.MessageID == "" {
			row.MessageID = m.ID
		}
		// Whether the address is on the list now is kept with the event.
		e.Suppressed = s.inForce(domain, address)
	}
	s.events = append(s.events, e)
	s.owe(e, store.DomainOf(m.Sender))
	if store.Moves(m.State, e.Kind, newer) {
		to := store.StateOf[e.Kind]
		if to == store.StateFailed {
			for at, row := range s.idems {
				if row.messageID == m.ID {
					delete(s.idems, at)
				}
			}
		}
		s.setState(m.ID, to, "")
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

// EventsOf implements store.Events.
func (s *Store) EventsOf(_ context.Context, messageID string) ([]store.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Event
	for _, e := range s.events {
		if e.MessageID == messageID {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}
