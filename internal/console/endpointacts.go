package console

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// did answers a request that changed an endpoint, or could not: with
// the page it leads to, which says what happened, or with what failed.
// An endpoint the signed-in key does not manage is, to it, not there:
// the webhook service says so for every one of these.
func (c *console) did(w http.ResponseWriter, r *http.Request, err error, word, next string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such webhook endpoint.")
	case err != nil:
		c.Log.Error("console: a webhook endpoint could not be changed", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "That could not be done. Try again shortly.")
	default:
		c.flash(w, word)
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

// small reads a request's small form, or answers that it is too much.
func (c *console) small(w http.ResponseWriter, r *http.Request) bool {
	if !form(w, r) {
		c.fail(w, r, http.StatusUnprocessableEntity, "That is more than this form takes.")
		return false
	}
	return true
}

// enableEndpoint turns an endpoint on or off. The form says which, so
// that sending it twice changes nothing more; one that says neither
// changes nothing.
func (c *console) enableEndpoint(w http.ResponseWriter, r *http.Request) {
	if !c.small(w, r) {
		return
	}
	asked := r.PostFormValue("enabled")
	if asked != "on" && asked != "off" {
		c.fail(w, r, http.StatusUnprocessableEntity, "That form does not say whether to turn the endpoint on or off.")
		return
	}
	on, id := asked == "on", r.PathValue("id")
	_, err := c.Webhooks.Update(r.Context(), who(r).Domain, id, webhook.Change{Enabled: &on})
	if err == nil {
		c.Log.Info("console: a webhook endpoint was turned on or off", "by", who(r).Prefix, "endpoint", id, "enabled", on)
	}
	c.did(w, r, err, "endpoint-"+asked, listed(r))
}

// testEndpoint sends an endpoint a signed test webhook now. What it met
// is said by the page it leads to, so that asking for that page again
// sends nothing more.
func (c *console) testEndpoint(w http.ResponseWriter, r *http.Request) {
	if !c.small(w, r) {
		return
	}
	id := r.PathValue("id")
	result, err := c.Webhooks.Test(r.Context(), who(r).Domain, id)
	if err == nil {
		c.Log.Info("console: a webhook endpoint was tested", "by", who(r).Prefix, "endpoint", id,
			"status", result.Status, "failure", result.Failure)
	}
	c.did(w, r, err, "tested:"+strconv.Itoa(result.Status)+":"+result.Failure, listed(r))
}

// retryDelivery has a delivery tried again: now, or for an endpoint
// that is off, when it is turned on.
func (c *console) retryDelivery(w http.ResponseWriter, r *http.Request) {
	if !c.small(w, r) {
		return
	}
	e, err := c.Webhooks.Get(r.Context(), who(r).Domain, r.PathValue("id"))
	if err != nil {
		c.did(w, r, err, "", "")
		return
	}
	err = c.Webhooks.Retry(r.Context(), who(r).Domain, e.ID, r.PathValue("delivery"))
	word := "retried"
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Delivered meanwhile, or never this endpoint's.
		err, word = nil, "retry-gone"
	case err == nil && !e.Enabled:
		word = "retry-off"
	}
	if err == nil && word != "retry-gone" {
		c.Log.Info("console: a webhook delivery was retried", "by", who(r).Prefix, "endpoint", e.ID)
	}
	c.did(w, r, err, word, listed(r))
}

// removePage asks before an endpoint is removed.
func (c *console) removePage(w http.ResponseWriter, r *http.Request) {
	if e, ok := c.endpoint(w, r); ok {
		c.render(w, r, http.StatusOK, "remove", e)
	}
}

// removeEndpoint removes an endpoint and all it was owed.
func (c *console) removeEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := c.Webhooks.Delete(r.Context(), who(r).Domain, id)
	if err == nil {
		c.Log.Info("console: a webhook endpoint was removed", "by", who(r).Prefix, "endpoint", id)
	}
	c.did(w, r, err, "endpoint-removed", Root+"/webhooks")
}
