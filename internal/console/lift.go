package console

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/suppress"
)

// liftView is the page that asks before a suppression, or a sending
// domain's suppressions, are lifted.
type liftView struct {
	Domain, Address string
	// All says it is every suppression of the domain, of Reason or, for
	// "", of any.
	All    bool
	Reason string
	// In is the sending domain the list was narrowed to, For the handle
	// of the address it was narrowed to, and List where that list is:
	// all lead back to it.
	In, For, List string
}

// liftSuppression lets an address be sent to again from a sending
// domain. It asks first, in the answer to the same form, so that the
// address is never in a URL.
func (c *console) liftSuppression(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		c.fail(w, r, http.StatusUnprocessableEntity, "That is more than this form takes.")
		return
	}
	in, handle, list := c.narrowed(r)
	address := strings.TrimSpace(r.PostFormValue("address"))
	// The domain it acts on is settled first: it is the one asked about,
	// lifted and logged.
	domain, err := c.suppressed(r).Domain(r.PostFormValue("domain"))
	if err != nil || domain == "" || address == "" {
		c.fail(w, r, http.StatusUnprocessableEntity, "That is not a suppression this key manages.")
		return
	}
	if r.PostFormValue("confirm") == "" {
		found, err := c.suppressed(r).Of(r.Context(), domain, address)
		switch {
		case err != nil:
			c.Log.Error("console: a suppression could not be read", "error", err)
			c.fail(w, r, http.StatusServiceUnavailable, "The suppression could not be read. Try again shortly.")
		case len(found) == 0:
			c.flash(w, "lifted-already")
			http.Redirect(w, r, list, http.StatusSeeOther)
		default:
			c.render(w, r, http.StatusOK, "lift", liftView{Domain: domain, Address: found[0].Address, In: in, For: handle, List: list})
		}
		return
	}
	err = c.suppressed(r).Lift(r.Context(), domain, address)
	var invalid *suppress.Invalid
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.flash(w, "lifted-already")
	case errors.As(err, &invalid), errors.Is(err, suppress.ErrDomain):
		c.fail(w, r, http.StatusUnprocessableEntity, "That is not a suppression this key manages.")
		return
	case err != nil:
		c.Log.Error("console: a suppression could not be lifted", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The suppression could not be lifted. Try again shortly.")
		return
	default:
		c.Log.Info("console: a suppression was lifted", "by", who(r).Prefix, "domain", domain)
		c.flash(w, "lifted")
	}
	http.Redirect(w, r, list, http.StatusSeeOther)
}

// liftSuppressions lifts every suppression of a sending domain, or those
// of one reason: for the day a fault suppressed people wrongly. It asks
// first; how many it lifted is said by the page it then leads to, so
// that asking for its own answer again lifts nothing more.
func (c *console) liftSuppressions(w http.ResponseWriter, r *http.Request) {
	v := suppressionsView{}
	refuse := func(code int, why string) {
		v.LiftProblem = why
		c.suppressionsShown(w, r, code, v)
	}
	if !form(w, r) {
		refuse(http.StatusUnprocessableEntity, "That is more than this form takes.")
		return
	}
	var handle, list string
	v.Domain, handle, list = c.narrowed(r)
	asked, reason := strings.TrimSpace(r.PostFormValue("domain")), r.PostFormValue("reason")
	if v.Lift.Reason = reason; len(asked) <= 400 {
		v.Lift.Domain = strings.ToValidUTF8(asked, "")
	}
	domain, err := c.suppressed(r).Domain(asked)
	switch {
	case errors.Is(err, suppress.ErrDomain):
		refuse(http.StatusForbidden, "The key you are signed in with manages one sending domain, and can only lift its suppressions.")
		return
	case err != nil || domain == "":
		refuse(http.StatusUnprocessableEntity, "Name the sending domain whose suppressions to lift, such as mail.example.com.")
		return
	case reason != "" && !slices.Contains(store.Reasons, reason):
		v.Lift.Reason = ""
		refuse(http.StatusUnprocessableEntity, "Choose a reason from the list.")
		return
	}
	if r.PostFormValue("confirm") == "" {
		c.render(w, r, http.StatusOK, "lift", liftView{Domain: domain, All: true, Reason: reason, In: v.Domain, For: handle, List: list})
		return
	}
	n, err := c.suppressed(r).LiftAll(r.Context(), domain, reason)
	if err != nil {
		c.Log.Error("console: suppressions could not be lifted", "error", err)
		refuse(http.StatusServiceUnavailable, "The suppressions could not be lifted. Try again shortly.")
		return
	}
	c.Log.Info("console: a domain's suppressions were lifted", "by", who(r).Prefix, "domain", domain,
		"reason", reason, "lifted", n)
	c.flash(w, "lifted-all:"+strconv.Itoa(n))
	http.Redirect(w, r, list, http.StatusSeeOther)
}
