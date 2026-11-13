package api

import (
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

type deliveryView struct {
	// ID is the webhook's id as the endpoint was given it.
	ID      string `json:"id"`
	EmailID string `json:"email_id"`
	Type    string `json:"type"`
	// State is "pending", "delivered" or "failed" (given up).
	State    string `json:"state"`
	Attempts int    `json:"attempts"`
	// LastStatus is the endpoint's last answer; LastFailure why the last
	// try failed: "timeout", "refused", "dns", "tls", "blocked", "status"
	// or "other".
	LastStatus    int        `json:"last_status,omitempty"`
	LastFailure   string     `json:"last_failure,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	AttemptedAt   *time.Time `json:"attempted_at,omitempty"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
}

func deliveryOf(d store.Delivery) deliveryView {
	v := deliveryView{ID: d.ID, EmailID: d.MessageID, Type: webhook.TypeOf(d.Kind), State: d.State, Attempts: d.Attempts,
		LastStatus: d.LastStatus, LastFailure: d.LastFailure, CreatedAt: d.CreatedAt.UTC(),
		AttemptedAt: utc(d.AttemptedAt), DeliveredAt: utc(d.DeliveredAt)}
	if d.State == store.DeliveryPending {
		v.NextAttemptAt = utc(&d.NextAt)
	}
	return v
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	at := t.UTC()
	return &at
}

// listDeliveries is GET /webhooks/{id}/deliveries: what the endpoint was
// sent and how it went, newest first. ?state= narrows it to one state,
// ?limit= sizes the page, and ?after= takes the "next" of the page before.
func (a *app) listDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, ok := limitOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	list, next, err := a.Webhooks.Deliveries(r.Context(), scope(r), r.PathValue("id"), q.Get("state"), q.Get("after"), limit)
	if err != nil {
		a.failWebhook(w, err)
		return
	}
	data := make([]deliveryView, 0, len(list))
	for _, d := range list {
		data = append(data, deliveryOf(d))
	}
	out := map[string]any{"data": data}
	if next != "" {
		out["next"] = next
	}
	write(w, http.StatusOK, out)
}

// retryDelivery is POST /webhooks/{id}/deliveries/{delivery}/retry: try a
// webhook now, once more if it was given up, or without waiting for its
// next try if it is pending.
func (a *app) retryDelivery(w http.ResponseWriter, r *http.Request) {
	if err := a.Webhooks.Retry(r.Context(), scope(r), r.PathValue("id"), r.PathValue("delivery")); err != nil {
		a.failWebhook(w, err)
		return
	}
	write(w, http.StatusOK, map[string]string{"state": store.DeliveryPending})
}

// testWebhook is POST /webhooks/{id}/test: send the endpoint a signed
// webhook of type "webhook.test" now, and say how it answered. Only the
// status and the kind of failure are said, never the endpoint's own words:
// an answer read back would make this a way to read what the URL serves.
func (a *app) testWebhook(w http.ResponseWriter, r *http.Request) {
	res, err := a.Webhooks.Test(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		a.failWebhook(w, err)
		return
	}
	out := map[string]any{"delivered": res.Delivered()}
	if res.Status != 0 {
		out["status"] = res.Status
	}
	if res.Failure != "" {
		out["failure"] = res.Failure
	}
	write(w, http.StatusOK, out)
}
