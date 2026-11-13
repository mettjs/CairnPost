package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

type endpointView struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// Domain is the sending domain it hears about; absent for all.
	Domain string `json:"domain,omitempty"`
	// Events are the types it hears about; empty for all.
	Events      []string  `json:"events"`
	Description string    `json:"description,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	// Secret is the signing secret, said once: when the endpoint is made.
	Secret string `json:"secret,omitempty"`
}

func endpointOf(e store.Endpoint) endpointView {
	events := make([]string, 0, len(e.Kinds))
	for _, kind := range e.Kinds {
		events = append(events, webhook.TypeOf(kind))
	}
	return endpointView{ID: e.ID, URL: e.URL, Domain: e.Domain, Events: events, Description: e.Description,
		Enabled: e.Enabled, CreatedAt: e.CreatedAt.UTC()}
}

// failWebhook answers a webhook request that was not carried out.
func (a *app) failWebhook(w http.ResponseWriter, err error) {
	var invalid *webhook.Invalid
	switch {
	case errors.As(err, &invalid):
		fail(w, http.StatusUnprocessableEntity, "validation_error", invalid.Reason)
	case errors.Is(err, webhook.ErrDomain):
		fail(w, http.StatusForbidden, "domain_not_allowed",
			"This API key manages the webhook endpoints of its own sending domain only.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "There is no such webhook endpoint, or no such delivery of it.")
	default:
		a.Log.Error("api: a webhook request failed", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The webhook endpoints could not be reached. Try again shortly.")
	}
}

// scope is the sending domain a caller is bound to, "" for none.
func scope(r *http.Request) string { return callerOf(r).key.Domain }

// createWebhook is POST /webhooks: have a URL told what becomes of emails.
// The answer carries the signing secret, and no later answer does.
func (a *app) createWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL         string   `json:"url"`
		Domain      string   `json:"domain"`
		Events      []string `json:"events"`
		Description string   `json:"description"`
	}
	if !a.body(w, r, &body, `The body must be JSON with "url"; "domain", "events" and "description" are optional.`) {
		return
	}
	e, err := a.Webhooks.Create(r.Context(), scope(r), webhook.New{URL: body.URL, Domain: body.Domain,
		Types: body.Events, Description: body.Description})
	if err != nil {
		a.failWebhook(w, err)
		return
	}
	view := endpointOf(e)
	view.Secret = webhook.Reveal(e.Secret)
	write(w, http.StatusCreated, view)
}

// listWebhooks is GET /webhooks.
func (a *app) listWebhooks(w http.ResponseWriter, r *http.Request) {
	list, err := a.Webhooks.List(r.Context(), scope(r))
	if err != nil {
		a.failWebhook(w, err)
		return
	}
	data := make([]endpointView, 0, len(list))
	for _, e := range list {
		data = append(data, endpointOf(e))
	}
	write(w, http.StatusOK, map[string]any{"data": data})
}

// getWebhook is GET /webhooks/{id}.
func (a *app) getWebhook(w http.ResponseWriter, r *http.Request) {
	e, err := a.Webhooks.Get(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		a.failWebhook(w, err)
		return
	}
	write(w, http.StatusOK, endpointOf(e))
}

// updateWebhook is PATCH /webhooks/{id}: change what is named, leave the
// rest. "enabled": false stops it being told anything new.
func (a *app) updateWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL         *string   `json:"url"`
		Events      *[]string `json:"events"`
		Description *string   `json:"description"`
		Enabled     *bool     `json:"enabled"`
	}
	if !a.body(w, r, &body, `The body must be JSON with any of "url", "events", "description" and "enabled".`) {
		return
	}
	e, err := a.Webhooks.Update(r.Context(), scope(r), r.PathValue("id"),
		webhook.Change{URL: body.URL, Types: body.Events, Description: body.Description, Enabled: body.Enabled})
	if err != nil {
		a.failWebhook(w, err)
		return
	}
	write(w, http.StatusOK, endpointOf(e))
}

// deleteWebhook is DELETE /webhooks/{id}.
func (a *app) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if err := a.Webhooks.Delete(r.Context(), scope(r), r.PathValue("id")); err != nil {
		a.failWebhook(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
