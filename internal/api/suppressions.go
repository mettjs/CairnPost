package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/suppress"
)

type suppressionView struct {
	Domain  string `json:"domain"`
	Address string `json:"address"`
	// Reason is "bounce", "complaint" or "manual".
	Reason string `json:"reason"`
	// EmailID is the email whose bounce or complaint caused it.
	EmailID   string    `json:"email_id,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func viewOf(s store.Suppression) suppressionView {
	return suppressionView{s.Domain, s.Address, s.Reason, s.MessageID, s.Note, s.CreatedAt.UTC()}
}

// suppressions is the list as the caller's key may use it: a key bound
// to a sending domain manages that domain's and no other's.
func (a *app) suppressions(r *http.Request) suppress.Scoped {
	return a.Suppress.For(callerOf(r).key.Domain)
}

// failSuppress answers a suppression request that was not carried out.
func (a *app) failSuppress(w http.ResponseWriter, err error) {
	var invalid *suppress.Invalid
	switch {
	case errors.As(err, &invalid):
		fail(w, http.StatusUnprocessableEntity, "validation_error", invalid.Reason)
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "That address is not suppressed for that domain.")
	case errors.Is(err, suppress.ErrDomain):
		fail(w, http.StatusForbidden, "domain_not_allowed",
			"This API key manages the suppressions of its own sending domain only.")
	default:
		a.Log.Error("api: a suppression request failed", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The suppression list could not be reached. Try again shortly.")
	}
}

// listSuppressions is GET /suppressions: the addresses not sent to, in
// order of domain and address. ?domain= narrows it to one sending domain,
// ?limit= sizes the page, and ?after= takes the "next" of the page before.
func (a *app) listSuppressions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := limitOf(w, r)
	if !ok {
		return
	}
	page, next, err := a.suppressions(r).List(r.Context(), q.Get("domain"), q.Get("after"), limit)
	if err != nil {
		a.failSuppress(w, err)
		return
	}
	data := make([]suppressionView, 0, len(page))
	for _, s := range page {
		data = append(data, viewOf(s))
	}
	out := map[string]any{"data": data}
	if next != "" {
		out["next"] = next
	}
	write(w, http.StatusOK, out)
}

// addSuppression is POST /suppressions: stop sending to an address from a
// domain. Adding one that is there already changes nothing and says so.
func (a *app) addSuppression(w http.ResponseWriter, r *http.Request) {
	var body suppressBody
	if !a.body(w, r, &body, `The body must be JSON with "domain" and "address"; "note" is optional.`) {
		return
	}
	s, added, err := a.suppressions(r).Add(r.Context(), body.Domain, body.Address, body.Note)
	if err != nil {
		a.failSuppress(w, err)
		return
	}
	code := http.StatusCreated
	if !added {
		code = http.StatusOK // it was suppressed already; nothing changed
	}
	write(w, code, map[string]any{"domain": s.Domain, "address": s.Address, "added": added})
}

// suppressBody is what POST /suppressions takes.
type suppressBody struct {
	Domain  string `json:"domain"`
	Address string `json:"address"`
	Note    string `json:"note"`
}
