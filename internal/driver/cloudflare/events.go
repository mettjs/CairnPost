package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// Events reads the delivery events Cloudflare publishes to a Queue, by
// HTTP pull. The queue is one an Email Sending event subscription writes
// to, with an HTTP pull consumer; the token needs the Queues permission.
type Events struct {
	d    *Driver
	path string
}

// Events returns the reader of a queue's events.
func (d *Driver) Events(queueID string) *Events {
	return &Events{d: d, path: "/queues/" + queueID + "/messages"}
}

var _ driver.Source = (*Events)(nil)

// pullLimit bounds a pull's answer: a batch of the largest messages a queue
// holds, with room to spare.
const pullLimit = 32 << 20

// Take implements driver.Source.
func (e *Events) Take(ctx context.Context, max int, lease time.Duration) ([]driver.Received, error) {
	a, err := e.d.askUpTo(ctx, pullLimit, http.MethodPost, e.path+"/pull", map[string]any{
		"batch_size": max, "visibility_timeout_ms": lease.Milliseconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("the events queue could not be reached: %w", err)
	}
	if err := refused(a); err != nil {
		return nil, err
	}
	var res struct {
		Messages []struct {
			Body     string `json:"body"`
			LeaseID  string `json:"lease_id"`
			Attempts int    `json:"attempts"`
			Stamp    int64  `json:"timestamp_ms"`
		} `json:"messages"`
	}
	if json.Unmarshal(a.result, &res) != nil {
		return nil, errors.New("cloudflare: the queue's answer could not be read")
	}
	out := make([]driver.Received, 0, len(res.Messages))
	for _, m := range res.Messages {
		event, ok := read(m.Body)
		if event.At.IsZero() && m.Stamp > 0 {
			// The event did not say when: the queue's own stamp is close.
			event.At = time.UnixMilli(m.Stamp).UTC()
		}
		out = append(out, driver.Received{Lease: m.LeaseID, Attempts: m.Attempts, Event: event, Readable: ok})
	}
	return out, nil
}

// Settle implements driver.Source.
func (e *Events) Settle(ctx context.Context, done, again []string, delay time.Duration) error {
	if len(done) == 0 && len(again) == 0 {
		return nil
	}
	type lease struct {
		LeaseID      string `json:"lease_id"`
		DelaySeconds int    `json:"delay_seconds,omitempty"`
	}
	acks, retries := make([]lease, 0, len(done)), make([]lease, 0, len(again))
	for _, id := range done {
		acks = append(acks, lease{LeaseID: id})
	}
	for _, id := range again {
		retries = append(retries, lease{LeaseID: id, DelaySeconds: int(delay.Seconds())})
	}
	a, err := e.d.ask(ctx, http.MethodPost, e.path+"/ack", map[string]any{"acks": acks, "retries": retries})
	if err != nil {
		return fmt.Errorf("the events queue could not be reached: %w", err)
	}
	return refused(a)
}

// refused says what a queue's refusal means to whoever runs CairnPost: the
// answer's kind, in words about the queue and its settings, not about mail.
func refused(a answer) error {
	err := a.refusal()
	var throttled *driver.Throttled
	switch {
	case err == nil:
		return nil
	case errors.As(err, &throttled):
		return fmt.Errorf("cloudflare: the events queue is being asked too often (status %d): %w",
			a.status, &driver.Throttled{After: throttled.After})
	case a.status == http.StatusNotFound:
		return errors.New("cloudflare: the events queue does not exist (status 404); " +
			"check CAIRNPOST_CLOUDFLARE_QUEUE_ID")
	case a.status == http.StatusUnauthorized, a.status == http.StatusForbidden:
		return fmt.Errorf("cloudflare: the events queue refused the API token (status %d); "+
			"it needs the Queues permission", a.status)
	}
	return fmt.Errorf("cloudflare: the events queue answered status %d", a.status)
}
