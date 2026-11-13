package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// delivery is a row of what the endpoints are owed.
type delivery struct {
	store.Delivery
	event store.Event
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
		if len(out) == max || !end.Enabled || slices.Contains(busy, end.ID) || d.State != store.DeliveryPending ||
			d.NextAt.After(now) || !free || taken[end.ID] == perEndpoint {
			continue
		}
		taken[end.ID]++
		until := now.Add(lease)
		d.Attempts, d.AttemptedAt, d.taken = d.Attempts+1, &now, &until
		out = append(out, store.Due{Delivery: d.Delivery, Endpoint: *end, Event: d.event, Message: s.messages[d.MessageID]})
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
	}
	return nil
}

// DeliveryMissed implements store.Webhooks.
func (s *Store) DeliveryMissed(_ context.Context, id string, attempt, status int, failure string, retryIn time.Duration, final bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.delivery(id); d != nil && d.State == store.DeliveryPending && d.Attempts == attempt {
		d.NextAt, d.LastStatus, d.LastFailure, d.taken = s.Now().Add(retryIn), status, failure, nil
		if final {
			d.State = store.DeliveryFailed
		}
	}
	return nil
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
	past := before == ""
	for i := len(s.deliveries) - 1; i >= 0 && len(out) < limit; i-- {
		d := s.deliveries[i]
		if past && d.EndpointID == endpointID && (state == "" || d.State == state) {
			out = append(out, d.Delivery)
		}
		past = past || d.ID == before
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
