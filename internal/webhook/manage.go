package webhook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mettjs/cairnpost/internal/page"
	"github.com/mettjs/cairnpost/internal/store"
)

// Get answers an endpoint; store.ErrNotFound for none the caller manages.
func (s *Service) Get(ctx context.Context, scope, id string) (store.Endpoint, error) {
	e, err := s.store.Endpoint(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && scope != "" && e.Domain != scope:
		// Another domain's endpoint is, to this caller, not there.
		return store.Endpoint{}, store.ErrNotFound
	case err != nil:
		return store.Endpoint{}, fmt.Errorf("webhook: read an endpoint: %w", err)
	}
	return e, nil
}

// List answers the endpoints the caller manages, oldest first.
func (s *Service) List(ctx context.Context, scope string) ([]store.Endpoint, error) {
	list, err := s.store.ListEndpoints(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("webhook: list endpoints: %w", err)
	}
	return list, nil
}

// Change is what to change of an endpoint: nil leaves a part as it is.
type Change struct {
	URL         *string
	Types       *[]string
	Description *string
	Enabled     *bool
}

// Update changes an endpoint. Disabled, it is owed nothing new, and what
// it was owed waits until it is enabled again. It neither pauses nor
// resumes: Resume does.
func (s *Service) Update(ctx context.Context, scope, id string, c Change) (store.Endpoint, error) {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return store.Endpoint{}, err
	}
	change := store.EndpointChange{Enabled: c.Enabled}
	if c.URL != nil {
		address := strings.TrimSpace(*c.URL)
		if err := s.checkURL(address); err != nil {
			return store.Endpoint{}, err
		}
		change.URL = &address
	}
	if c.Types != nil {
		kinds, err := kindsOf(*c.Types)
		if err != nil {
			return store.Endpoint{}, err
		}
		change.Kinds = &kinds
	}
	if c.Description != nil {
		description := strings.TrimSpace(*c.Description)
		if err := checkDescription(description); err != nil {
			return store.Endpoint{}, err
		}
		change.Description = &description
	}
	e, err := s.store.UpdateEndpoint(ctx, id, change)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, fmt.Errorf("webhook: change an endpoint: %w", err)
	}
	return e, err
}

// Delete removes an endpoint and all it was owed.
func (s *Service) Delete(ctx context.Context, scope, id string) error {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return err
	}
	if err := s.store.DeleteEndpoint(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("webhook: delete an endpoint: %w", err)
	} else if err != nil {
		return err
	}
	return nil
}

// Deliveries answers a page of an endpoint's deliveries, newest first:
// those in a state, or all (""), after the cursor of the page before, or
// from the newest (""). It answers the cursor of the page after it, ""
// when there is none.
func (s *Service) Deliveries(ctx context.Context, scope, id, state, cursor string, limit int) ([]store.Delivery, string, error) {
	limit = page.Size(limit)
	switch {
	case state != "" && !slices.Contains(store.DeliveryStates, state):
		return nil, "", &Invalid{Field: "state", Reason: `"state" must be "pending", "delivered" or "failed", or left out for all.`}
	case !plain(cursor) || len(cursor) > 64:
		return nil, "", &Invalid{Field: "after", Reason: `"after" is not a cursor this list gave.`}
	}
	if _, err := s.Get(ctx, scope, id); err != nil {
		return nil, "", err
	}
	// One more than asked for says whether another page follows.
	list, err := s.store.ListDeliveries(ctx, id, state, cursor, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("webhook: list deliveries: %w", err)
	}
	if len(list) <= limit {
		return list, "", nil
	}
	return list[:limit], list[limit-1].ID, nil
}

// Retry makes a delivery due now: one that was given up is tried once
// more, one that is waiting for its next try stops waiting.
// store.ErrNotFound for none such, or one already delivered.
func (s *Service) Retry(ctx context.Context, scope, id, deliveryID string) error {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return err
	}
	if err := s.store.RetryDelivery(ctx, id, deliveryID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("webhook: retry a delivery: %w", err)
	} else if err != nil {
		return err
	}
	return nil
}

// Test sends an endpoint a signed webhook of type "webhook.test", once,
// now, and answers what it met. Nothing is kept of it.
func (s *Service) Test(ctx context.Context, scope, id string) (Result, error) {
	e, err := s.Get(ctx, scope, id)
	if err != nil {
		return Result{}, err
	}
	name := make([]byte, 12)
	_, _ = rand.Read(name)
	result := s.poster.Post(ctx, e.URL, keysOf(e), "test_"+hex.EncodeToString(name), testBody(s.now()))
	if result.Delivered() {
		// It answers: it is no longer failing. Not being able to write
		// that down does not make the test fail.
		_ = s.store.EndpointAnswered(ctx, e.ID)
	}
	return result, nil
}

// Rotate gives an endpoint a new signing key and answers the endpoint
// with it. For the service's overlap its webhooks are signed with the key
// it had as well, and with any before that still within their own time,
// so its receiver can change over without missing one.
func (s *Service) Rotate(ctx context.Context, scope, id string) (store.Endpoint, error) {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return store.Endpoint{}, err
	}
	e, err := s.store.RotateSecret(ctx, id, newSecret(), s.overlap)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, fmt.Errorf("webhook: change an endpoint's signing key: %w", err)
	}
	return e, err
}

// Resume ends an endpoint's pause: what it is owed is sent again, with
// what was given up while it failed. It answers whether the endpoint was
// paused; one that was not is left as it is.
func (s *Service) Resume(ctx context.Context, scope, id string) (store.Endpoint, bool, error) {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return store.Endpoint{}, false, err
	}
	e, resumed, err := s.store.ResumeEndpoint(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Endpoint{}, false, fmt.Errorf("webhook: resume an endpoint: %w", err)
	}
	return e, resumed, err
}
