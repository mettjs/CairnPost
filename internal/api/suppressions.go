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

// errOtherDomain is a key bound to one sending domain asking about another.
var errOtherDomain = errors.New("api: the key is bound to another sending domain")

// domainFor is the domain a caller may act on: the one asked for, which a
// key bound to a domain may only be its own. A bound key that names none
// gets its own.
func domainFor(r *http.Request, asked string) (string, error) {
	bound := callerOf(r).key.Domain
	switch {
	case bound == "":
		return asked, nil
	case asked == "" || store.Fold(asked) == bound:
		return bound, nil
	}
	return "", errOtherDomain
}

// failSuppress answers a suppression request that was not carried out.
func (a *app) failSuppress(w http.ResponseWriter, err error) {
	var invalid *suppress.Invalid
	switch {
	case errors.As(err, &invalid):
		fail(w, http.StatusUnprocessableEntity, "validation_error", invalid.Reason)
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "That address is not suppressed for that domain.")
	case errors.Is(err, errOtherDomain):
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
	domain, err := domainFor(r, q.Get("domain"))
	if err != nil {
		a.failSuppress(w, err)
		return
	}
	page, next, err := a.Suppress.List(r.Context(), domain, q.Get("after"), limit)
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
	var body struct {
		Domain  string `json:"domain"`
		Address string `json:"address"`
		Note    string `json:"note"`
	}
	if !a.body(w, r, &body, `The body must be JSON with "domain" and "address"; "note" is optional.`) {
		return
	}
	domain, err := domainFor(r, body.Domain)
	if err != nil {
		a.failSuppress(w, err)
		return
	}
	s, added, err := a.Suppress.Add(r.Context(), domain, body.Address, body.Note)
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
