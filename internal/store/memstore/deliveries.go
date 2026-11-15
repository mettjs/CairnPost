package memstore

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// delivery is a row of what the endpoints are owed.
type delivery struct {
	store.Delivery
	event store.Event
	// about is the suppression it tells of, for one that tells of no event.
	about *store.Suppression
	// came is the email that came it tells of, for one that tells of that.
	came *store.Inbound
	// taken is until when a try in progress holds it, nil for none.
	taken *time.Time
}

// owe writes what each endpoint that hears about an event is owed. The
// lock is held.
func (s *Store) owe(e store.Event, domain string) {
	for _, end := range s.endpoints {
		if end.Enabled && (end.Domain == "" || end.Domain == domain) && (len(end.Kinds) == 0 || slices.Contains(end.Kinds, e.Kind)) {
			s.deliveries = append(s.deliveries, &delivery{event: e, Delivery: store.Delivery{ID: s.next("dlv"),
				EndpointID: end.ID, Kind: e.Kind, MessageID: e.MessageID, State: store.DeliveryPending,
				NextAt: s.Now(), CreatedAt: s.Now()}})
		}
	}
}

// oweNotice writes what each endpoint is owed for an address suppressed
// because of an email with no record here. The lock is held.
func (s *Store) oweNotice(sup store.Suppression) {
	for _, end := range s.endpoints {
		if end.Enabled && (end.Domain == "" || end.Domain == sup.Domain) && (len(end.Kinds) == 0 || slices.Contains(end.Kinds, store.KindSuppressed)) {
			s.deliveries = append(s.deliveries, &delivery{about: &sup, Delivery: store.Delivery{ID: s.next("dlv"),
				EndpointID: end.ID, Kind: store.KindSuppressed, State: store.DeliveryPending,
				NextAt: s.Now(), CreatedAt: s.Now()}})
		}
	}
}

// TakeDeliveries implements store.Webhooks.
func (s *Store) TakeDeliveries(_ context.Context, max, perEndpoint int, busy []string, lease time.Duration) ([]store.Due, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, taken := s.Now(), map[string]int{}
	due := slices.Clone(s.deliveries)
	slices.SortStableFunc(due, func(a, b *delivery) int { return a.NextAt.Compare(b.NextAt) })
	var out []store.Due
	for _, d := range due {
		end := s.endpoint(d.EndpointID)
		free := d.taken == nil || !d.taken.After(now)
		if len(out) == max || !end.Enabled || end.PausedAt != nil || slices.Contains(busy, end.ID) || d.State != store.DeliveryPending ||
			d.NextAt.After(now) || !free || taken[end.ID] == perEndpoint {
			continue
		}
		// An email that came and whose time is up is told of no more.
		if d.came != nil {
			if _, shown := s.shownInbound(d.came.ID); !shown {
				continue
			}
		}
		taken[end.ID]++
		until := now.Add(lease)
		d.Attempts, d.AttemptedAt, d.taken = d.Attempts+1, &now, &until
		due := store.Due{Delivery: d.Delivery, Endpoint: s.shown(end), Suppression: d.about, Inbound: d.came}
		if d.about == nil && d.came == nil {
			// The email is named by its own id, and the address is the
			// one the event is about.
			due.Event, due.Message = d.event, s.messages[d.MessageID]
			due.Message.ID, due.Delivery.MessageID, due.Event.MessageID = due.Message.EmailID, due.Message.EmailID, due.Message.EmailID
		}
		out = append(out, due)
	}
	return out, nil
}

func (s *Store) delivery(id string) *delivery {
	for _, d := range s.deliveries {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// DeliveryDone implements store.Webhooks.
func (s *Store) DeliveryDone(_ context.Context, id string, status int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.delivery(id); d != nil && d.State != store.DeliveryDelivered {
		now := s.Now()
		d.State, d.DeliveredAt, d.LastStatus, d.LastFailure, d.taken = store.DeliveryDelivered, &now, status, "", nil
		// One paused meanwhile keeps when its failing began.
		if end := s.endpoint(d.EndpointID); end.PausedAt == nil {
			end.FailingSince = nil
		}
	}
	return nil
}

// DeliveryMissed implements store.Webhooks.
func (s *Store) DeliveryMissed(_ context.Context, id string, attempt, status int, failure string, retryIn time.Duration, final bool,
	pauseAfter time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.delivery(id)
	if d == nil || d.State != store.DeliveryPending || d.Attempts != attempt {
		return false, nil
	}
	now := s.Now()
	d.NextAt, d.LastStatus, d.LastFailure, d.taken = now.Add(retryIn), status, failure, nil
	if final {
		d.State = store.DeliveryFailed
	}
	end := s.endpoint(d.EndpointID)
	if end.PausedAt != nil {
		return false, nil
	}
	if end.FailingSince == nil {
		// The failing began when this try was made.
		tried := now
		if d.AttemptedAt != nil {
			tried = *d.AttemptedAt
		}
		end.FailingSince = &tried
	}
	// Paused only by a webhook given up, once the failing is old enough.
	if final && pauseAfter > 0 && now.Sub(*end.FailingSince) >= pauseAfter {
		end.PausedAt = &now
		return true, nil
	}
	return false, nil
}

// DeliveryReleased implements store.Webhooks.
func (s *Store) DeliveryReleased(_ context.Context, id string, attempt int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.delivery(id); d != nil && d.State == store.DeliveryPending && d.Attempts == attempt {
		d.Attempts, d.taken = d.Attempts-1, nil
	}
	return nil
}

// ListDeliveries implements store.Webhooks. Ids are made in order, so
// "older than" is "made before".
func (s *Store) ListDeliveries(_ context.Context, endpointID, state, before string, limit int) ([]store.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Delivery
	if before != "" && !strings.HasPrefix(before, "dlv-") {
		return nil, nil // an id no delivery could have, as in the database
	}
	for i := len(s.deliveries) - 1; i >= 0 && len(out) < limit; i-- {
		d := s.deliveries[i]
		// By id, not by meeting the row named: it may have been pruned.
		if (before == "" || d.ID < before) && d.EndpointID == endpointID && (state == "" || d.State == state) {
			shown := d.Delivery
			if m, kept := s.messages[shown.MessageID]; kept {
				shown.MessageID = m.EmailID
			}
			out = append(out, shown)
		}
	}
	return out, nil
}

// RetryDelivery implements store.Webhooks.
func (s *Store) RetryDelivery(_ context.Context, endpointID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.delivery(id)
	if d == nil || d.EndpointID != endpointID || d.State == store.DeliveryDelivered {
		return store.ErrNotFound
	}
	d.State, d.NextAt = store.DeliveryPending, s.Now()
	return nil
}
