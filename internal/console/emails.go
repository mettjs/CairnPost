package console

import (
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// shownEmails is how many emails one page of the list holds.
const shownEmails = 100

// emailsView is the list of emails and the form that narrows it.
type emailsView struct {
	Emails []store.Message
	// To and State are what the list is narrowed to, "" for not; For is
	// the handle that names To in this page's links and forms.
	To, State, For string
	States         []string
	// Problem says why the form was refused; Typed is what was typed.
	Problem, Typed string
	Unread         bool
	// Older is where the next page is, "" when this is the last; Paged
	// says this is not the first, and Newest is where the first is.
	Older, Newest string
	Paged         bool
	// Forgotten says the page named an address that is no longer kept.
	Forgotten bool
}

func (c *console) emailsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.emailsShown(w, r, http.StatusOK, emailsView{State: q.Get("state")}, q.Get("before"))
}

// emailsShown is the page with the emails the signed-in key may read,
// newest first.
func (c *console) emailsShown(w http.ResponseWriter, r *http.Request, code int, v emailsView, before string) {
	v.States, v.Paged = store.States, before != ""
	if v.State != "" && !slices.Contains(store.States, v.State) {
		// A state nobody chose from the form: a URL written by hand. The
		// list is shown without it.
		v.State, code = "", http.StatusUnprocessableEntity
		if v.Problem == "" {
			v.Problem = "There is no such state. Choose one from the list."
		}
	}
	to, handle, err := c.found(r)
	var list []store.Message
	v.Forgotten = forgotten(r, handle)
	if v.To, v.For = to, handle; err == nil {
		list, err = c.Mail.List(r.Context(), who(r), mail.Query{To: v.To, State: v.State, Before: before, Limit: shownEmails + 1})
	}
	if err != nil {
		c.Log.Error("console: the emails could not be read", "error", err)
		v.Unread = true
	}
	if len(list) > shownEmails {
		list = list[:shownEmails]
		v.Older = emailsAt(v.State, list[shownEmails-1].ID, v.For)
	}
	v.Emails, v.Newest = list, emailsAt(v.State, "", v.For)
	c.render(w, r, code, "emails", v)
}

// emailsAt is the list's address: narrowed to a state, from the email
// before on, to the address a handle names; each only if given.
func emailsAt(state, before, handle string) string {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if before != "" {
		q.Set("before", before)
	}
	return at("/emails", narrowedTo(q, handle))
}

// findEmails narrows the list: to an address, kept with the session and
// named in the URL by its handle, and to a state, kept in the URL. An
// empty form shows every email again.
func (c *console) findEmails(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		c.emailsShown(w, r, http.StatusUnprocessableEntity, emailsView{Problem: notAnAddress}, "")
		return
	}
	state := r.PostFormValue("state")
	if r.PostFormValue("clear") != "" {
		state = ""
	}
	handle, typed, err := c.look(r)
	if errors.Is(err, errNotAnAddress) {
		c.emailsShown(w, r, http.StatusUnprocessableEntity, emailsView{State: state, Typed: typed, Problem: notAnAddress}, "")
		return
	}
	if err != nil {
		c.Log.Error("console: what to look for could not be kept", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The list could not be narrowed. Try again shortly.")
		return
	}
	http.Redirect(w, r, emailsAt(state, "", handle), http.StatusSeeOther)
}
