package console

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// shownDeliveries is how many deliveries one page of an endpoint holds.
const shownDeliveries = 50

// endpointView is one endpoint: what it is, the form that changes it,
// and what it was sent.
type endpointView struct {
	Endpoint store.Endpoint
	Problem  string
	Form     hookForm
	// Deliveries are what it was sent, newest first: those in State, or
	// all. Older is where the next page is; Paged says this is not the
	// first.
	Deliveries []store.Delivery
	State      string
	// After is the cursor this page of them follows, "" for the newest.
	After  string
	States []string
	Older  string
	Paged  bool
	Unread bool
}

// endpoint answers the endpoint a request names, if the signed-in key
// manages it; otherwise it has answered the request, and says false.
// Another domain's endpoint is, to this key, not there.
func (c *console) endpoint(w http.ResponseWriter, r *http.Request) (store.Endpoint, bool) {
	e, err := c.Webhooks.Get(r.Context(), who(r).Domain, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such webhook endpoint.")
	case err != nil:
		c.Log.Error("console: a webhook endpoint could not be read", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The endpoint could not be read. Try again shortly.")
	}
	return e, err == nil
}

// formOf is an endpoint as its form holds it.
func formOf(e store.Endpoint) hookForm {
	var types []string
	for _, kind := range e.Kinds {
		types = append(types, webhook.TypeOf(kind))
	}
	return hookForm{URL: e.URL, Description: e.Description, All: len(types) == 0, Types: choices(types)}
}

// cursorShape is what a cursor of a list of deliveries can look like.
var cursorShape = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// endpointURL is an endpoint's page, at a page of its deliveries: those
// in a state if that is one, after a cursor if that could be one.
func endpointURL(id, state, after string) string {
	at, q := Root+"/webhooks/"+url.PathEscape(id), url.Values{}
	if slices.Contains(store.DeliveryStates, state) {
		q.Set("state", state)
	}
	if cursorShape.MatchString(after) {
		q.Set("after", after)
	}
	if len(q) > 0 {
		at += "?" + q.Encode()
	}
	return at
}

// listed is the page of deliveries a form was sent from, which what the
// form does leads back to.
func listed(r *http.Request) string {
	return endpointURL(r.PathValue("id"), r.PostFormValue("in"), r.PostFormValue("at"))
}

func (c *console) endpointPage(w http.ResponseWriter, r *http.Request) {
	if e, ok := c.endpoint(w, r); ok {
		c.endpointShown(w, r, http.StatusOK, endpointView{Endpoint: e, Form: formOf(e)})
	}
}

// endpointShown is an endpoint's page with a page of its deliveries.
func (c *console) endpointShown(w http.ResponseWriter, r *http.Request, code int, v endpointView) {
	q := r.URL.Query()
	after := q.Get("after")
	v.States, v.Paged, v.After = store.DeliveryStates, after != "", after
	if v.State = q.Get("state"); !slices.Contains(store.DeliveryStates, v.State) {
		v.State = "" // a state nobody chose from the page narrows nothing
	}
	list, next, err := c.Webhooks.Deliveries(r.Context(), who(r).Domain, v.Endpoint.ID, v.State, after, shownDeliveries)
	var invalid *webhook.Invalid
	switch {
	case errors.As(err, &invalid):
		list, v.After = nil, "" // a cursor nobody was given: nothing follows it
	case errors.Is(err, store.ErrNotFound):
		// Removed since it was read.
		c.fail(w, r, http.StatusNotFound, "There is no such webhook endpoint.")
		return
	case err != nil:
		c.Log.Error("console: an endpoint's deliveries could not be read", "error", err)
		v.Unread = true
	}
	if v.Deliveries = list; next != "" {
		v.Older = endpointURL(v.Endpoint.ID, v.State, next)
	}
	c.render(w, r, code, "endpoint", v)
}

// updateEndpoint changes where an endpoint is, what it hears about and
// how it is described.
func (c *console) updateEndpoint(w http.ResponseWriter, r *http.Request) {
	e, ok := c.endpoint(w, r)
	if !ok {
		return
	}
	refuse := func(code int, why string, f hookForm) {
		c.endpointShown(w, r, code, endpointView{Endpoint: e, Problem: why, Form: f})
	}
	if !form(w, r) {
		refuse(http.StatusUnprocessableEntity, "That is more than this form takes.", formOf(e))
		return
	}
	f, types, problem := hookRead(r)
	if problem != "" {
		refuse(http.StatusUnprocessableEntity, problem, f)
		return
	}
	if types == nil {
		types = []string{} // every event: said, not left as it was
	}
	address, description := r.PostFormValue("url"), r.PostFormValue("description")
	_, err := c.Webhooks.Update(r.Context(), who(r).Domain, e.ID, webhook.Change{URL: &address, Types: &types, Description: &description})
	var invalid *webhook.Invalid
	switch {
	case errors.As(err, &invalid):
		refuse(http.StatusUnprocessableEntity, hookProblem(invalid), f)
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such webhook endpoint.")
	case err != nil:
		c.Log.Error("console: a webhook endpoint could not be changed", "error", err)
		refuse(http.StatusServiceUnavailable, "The endpoint could not be changed. Try again shortly.", f)
	default:
		c.Log.Info("console: a webhook endpoint was changed", "by", who(r).Prefix, "endpoint", e.ID)
		c.flash(w, "endpoint-changed")
		http.Redirect(w, r, endpointURL(e.ID, "", ""), http.StatusSeeOther)
	}
}
