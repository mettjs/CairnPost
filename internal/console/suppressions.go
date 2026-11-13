package console

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/suppress"
)

// shownSuppressions is how many suppressions the page lists: the first,
// in order of sending domain and address. A server with more has them
// all on the command line.
const shownSuppressions = 500

// suppressionsView is the list of addresses not sent to, the form that
// narrows it and the forms that change it.
type suppressionsView struct {
	List []store.Suppression
	// To is the address the list is narrowed to, Domain the sending
	// domain; "" for not. Bound is the one domain the signed-in key
	// manages, "" for all.
	To, Domain, Bound string
	Reasons           []string
	// Shown is how many rows the page lists at most.
	Shown int
	// Problem says why narrowing was refused; Typed and TypedDomain are
	// what was typed.
	Problem, Typed, TypedDomain string
	// AddProblem says why adding was refused; Add is what was typed.
	AddProblem string
	Add        struct{ Domain, Address, Note string }
	// LiftProblem says why lifting a domain's suppressions was refused;
	// Lift is what was asked.
	LiftProblem  string
	Lift         struct{ Domain, Reason string }
	Unread, More bool
}

// suppressed is the list as the signed-in key may use it.
func (c *console) suppressed(r *http.Request) suppress.Scoped { return c.Suppress.For(who(r).Domain) }

// notADomain is what a page is told that was asked for a sending domain
// the signed-in key does not manage, or for what is no domain.
const notADomain = "That is not a sending domain this key manages, such as mail.example.com."

func (c *console) suppressionsPage(w http.ResponseWriter, r *http.Request) {
	v, code := suppressionsView{}, http.StatusOK
	domain, err := c.suppressed(r).Domain(r.URL.Query().Get("domain"))
	if err != nil {
		// A domain nobody could have chosen from the page: a URL written
		// by hand. The list is shown without it.
		v.Problem, code = notADomain+" The list is shown without it.", http.StatusUnprocessableEntity
	}
	if v.Domain = domain; domain == v.boundTo(r) {
		v.Domain = "" // a bound key's own domain narrows nothing
	}
	c.suppressionsShown(w, r, code, v)
}

// boundTo is the domain the signed-in key is bound to.
func (suppressionsView) boundTo(r *http.Request) string { return who(r).Domain }

// suppressionsShown is the page with the suppressions in force that the
// signed-in key manages: those of the address looked for, or the first
// of them all. v.Domain is a domain already checked.
func (c *console) suppressionsShown(w http.ResponseWriter, r *http.Request, code int, v suppressionsView) {
	v.Bound, v.Reasons, v.Shown = who(r).Domain, store.Reasons, shownSuppressions
	to, err := c.found(r)
	if v.To = to; err == nil && to != "" {
		// An address has one row for a sending domain at most.
		v.List, err = c.suppressed(r).Of(r.Context(), v.Domain, to)
	} else if err == nil {
		var next string
		v.List, next, err = c.suppressed(r).List(r.Context(), v.Domain, "", shownSuppressions)
		v.More = next != ""
	}
	if err != nil {
		c.Log.Error("console: the suppressions could not be read", "error", err)
		v.List, v.Unread = nil, true
	}
	c.render(w, r, code, "suppressions", v)
}

// narrowed answers the sending domain a form says the list it was sent
// from is narrowed to, checked, and the list's address with it: so that
// what a form does leads back to the list as it was. A domain that is
// none is no narrowing.
func (c *console) narrowed(r *http.Request) (domain, list string) {
	list = Root + "/suppressions"
	domain, err := c.suppressed(r).Domain(r.PostFormValue("in"))
	if err != nil || domain == "" || domain == who(r).Domain {
		return "", list
	}
	return domain, list + "?" + url.Values{"domain": {domain}}.Encode()
}

// findSuppressions narrows the list: to an address, kept with the
// session, and to a sending domain, kept in the URL once it is known to
// be a domain: an address typed in the wrong box never reaches a URL.
func (c *console) findSuppressions(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		c.suppressionsShown(w, r, http.StatusUnprocessableEntity, suppressionsView{Problem: notAnAddress})
		return
	}
	asked := strings.TrimSpace(r.PostFormValue("domain"))
	if r.PostFormValue("clear") != "" {
		asked = ""
	}
	domain, err := c.suppressed(r).Domain(asked)
	if err != nil {
		if asked = strings.ToValidUTF8(asked, ""); len(asked) > 400 {
			asked = ""
		}
		c.suppressionsShown(w, r, http.StatusUnprocessableEntity, suppressionsView{Problem: notADomain,
			Typed: strings.ToValidUTF8(strings.TrimSpace(r.PostFormValue("to")), ""), TypedDomain: asked})
		return
	}
	typed, err := c.look(r)
	if errors.Is(err, errNotAnAddress) {
		c.suppressionsShown(w, r, http.StatusUnprocessableEntity, suppressionsView{Typed: typed, TypedDomain: domain, Problem: notAnAddress})
		return
	}
	if err != nil {
		c.Log.Error("console: what to look for could not be kept", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The list could not be narrowed. Try again shortly.")
		return
	}
	next := Root + "/suppressions"
	if domain != "" && domain != who(r).Domain {
		next += "?" + url.Values{"domain": {domain}}.Encode()
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// addProblems are the form's own words for each part the suppression
// service can refuse.
var addProblems = map[string]string{
	"domain":  "Name the sending domain the address is suppressed for, such as mail.example.com.",
	"address": "Give one address with no name beside it, such as ana@example.org.",
	"note":    "The note must be one line of at most 500 bytes.",
}

// addSuppression stops sending to an address from a sending domain.
func (c *console) addSuppression(w http.ResponseWriter, r *http.Request) {
	v := suppressionsView{}
	if !form(w, r) {
		v.AddProblem = "That is more than this form takes. " + addProblems["note"]
		c.suppressionsShown(w, r, http.StatusUnprocessableEntity, v)
		return
	}
	var list string
	v.Domain, list = c.narrowed(r)
	clean := func(field string) string { return strings.ToValidUTF8(strings.TrimSpace(r.PostFormValue(field)), "") }
	v.Add.Domain, v.Add.Address, v.Add.Note = clean("domain"), clean("address"), clean("note")
	s, added, err := c.suppressed(r).Add(r.Context(), r.PostFormValue("domain"), r.PostFormValue("address"), r.PostFormValue("note"))
	var invalid *suppress.Invalid
	switch {
	case errors.As(err, &invalid):
		if v.AddProblem = addProblems[invalid.Field]; v.AddProblem == "" {
			v.AddProblem = "That cannot be suppressed as it is written."
		}
		c.suppressionsShown(w, r, http.StatusUnprocessableEntity, v)
	case errors.Is(err, suppress.ErrDomain):
		v.AddProblem = "The key you are signed in with manages one sending domain, and can only suppress addresses for it."
		c.suppressionsShown(w, r, http.StatusForbidden, v)
	case err != nil:
		c.Log.Error("console: an address could not be suppressed", "error", err)
		v.AddProblem = "The address could not be suppressed. Try again shortly."
		c.suppressionsShown(w, r, http.StatusServiceUnavailable, v)
	default:
		word := "suppressed-already"
		if added {
			// Who did it, and for which domain: never to whom.
			c.Log.Info("console: an address was suppressed", "by", who(r).Prefix, "domain", s.Domain)
			word = "suppressed"
		}
		c.flash(w, word)
		http.Redirect(w, r, list, http.StatusSeeOther)
	}
}
