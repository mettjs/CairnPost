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
	e, err := c.Webhooks.Update(r.Context(), who(r).Domain, id, webhook.Change{Enabled: &on})
	word := "endpoint-" + asked
	if err == nil {
		c.Log.Info("console: a webhook endpoint was turned on or off", "by", who(r).Prefix, "endpoint", id, "enabled", on)
		if webhook.StateOf(e) == webhook.StatePaused {
			word = "endpoint-on-paused" // on again, and paused as it was
		}
	}
	c.did(w, r, err, word, listed(r))
}

// resumeEndpoint ends an endpoint's pause. One that is not paused is
// left as it is, and the page says so.
func (c *console) resumeEndpoint(w http.ResponseWriter, r *http.Request) {
	if !c.small(w, r) {
		return
	}
	id := r.PathValue("id")
	_, resumed, err := c.Webhooks.Resume(r.Context(), who(r).Domain, id)
	word := "endpoint-not-paused"
	if err == nil && resumed {
		c.Log.Info("console: a webhook endpoint was resumed", "by", who(r).Prefix, "endpoint", id)
		word = "endpoint-resumed"
	}
	c.did(w, r, err, word, listed(r))
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
	case err == nil && webhook.StateOf(e) == webhook.StateOff:
		word = "retry-off"
	case err == nil && webhook.StateOf(e) == webhook.StatePaused:
		word = "retry-paused"
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

// secretView is the page that changes an endpoint's signing secret:
// asking first, and then, once, the new secret.
type secretView struct {
	Endpoint store.Endpoint
	// Secret is the new secret, "" while asking.
	Secret string
	// Overlap is how long both secrets sign, in words; "" when the old
	// one stops at once.
	Overlap string
}

// secretPage asks before an endpoint's signing secret is changed.
func (c *console) secretPage(w http.ResponseWriter, r *http.Request) {
	if e, ok := c.endpoint(w, r); ok {
		v := secretView{Endpoint: e}
		if overlap := c.Webhooks.Overlap(); overlap > 0 {
			v.Overlap = span(overlap)
		}
		c.render(w, r, http.StatusOK, "secret", v)
	}
}

// rotateSecret changes an endpoint's signing secret and shows the new
// one, this once: on the answer itself, never on a page asked for again.
func (c *console) rotateSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := c.Webhooks.Rotate(r.Context(), who(r).Domain, id)
	if err != nil {
		c.did(w, r, err, "", "")
		return
	}
	// The endpoint's id and nothing of the secret.
	c.Log.Info("console: a webhook endpoint's signing secret was changed", "by", who(r).Prefix, "endpoint", id)
	c.render(w, r, http.StatusOK, "secret", secretView{Endpoint: e, Secret: webhook.Reveal(e.Secret)})
}
