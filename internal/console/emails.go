package console

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

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
	// Chips narrow the list to a state.
	Chips []chip
	// Problem says why the form was refused; Typed is what was typed.
	Problem, Typed string
	Unread         bool
	// Older is where the next page is, "" when this is the last; Paged
	// says this is not the first, and Newest is where the first is.
	Older, Newest string
	Paged         bool
	// Forgotten says the page named an address that is no longer kept.
	Forgotten bool
	// Tabs are the page's parts: the emails on record, and those that
	// wait to be sent. Tab is the one shown, "" for the first.
	Tabs tabs
	Tab  string
	// Waiting are the emails that wait to be sent, soonest first; More
	// says there are more of them than are shown.
	Waiting []store.Held
	More    bool
}

func (c *console) emailsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if tabOf(r, "", "scheduled") != "" {
		c.scheduledShown(w, r)
		return
	}
	c.emailsShown(w, r, http.StatusOK, emailsView{State: q.Get("state")}, q.Get("before"))
}

// emailTabs are the parts of the emails page, the second with how many
// emails wait. A count that cannot be read is left unsaid.
func (c *console) emailTabs(r *http.Request, on, handle string) tabs {
	n, err := c.Mail.Waiting(r.Context(), who(r).Domain)
	if err != nil {
		c.Log.Error("console: the emails waiting to be sent could not be counted", "error", err)
	}
	return tabsOf("Emails", "/emails", narrowedTo(url.Values{}, handle), on,
		tab{Label: "On record"}, tab{Key: "scheduled", Label: "Scheduled", N: n})
}

// scheduledShown is the part of the page with the emails that wait to
// be sent, soonest first. It reads no email on record.
func (c *console) scheduledShown(w http.ResponseWriter, r *http.Request) {
	v := emailsView{Tab: "scheduled", For: handleOf(r)}
	list, err := c.Mail.Scheduled(r.Context(), who(r), time.Time{}, shownEmails+1)
	if err != nil {
		c.Log.Error("console: the emails waiting to be sent could not be read", "error", err)
		v.Unread = true
	}
	if v.More = len(list) > shownEmails; v.More {
		list = list[:shownEmails]
	}
	v.Waiting, v.Tabs = list, c.emailTabs(r, v.Tab, v.For)
	c.render(w, r, http.StatusOK, "emails", v)
}

// emailsShown is the page with the emails the signed-in key may read,
// newest first.
func (c *console) emailsShown(w http.ResponseWriter, r *http.Request, code int, v emailsView, before string) {
	v.Paged = before != ""
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
	v.Emails, v.Newest, v.Chips = list, emailsAt(v.State, "", v.For), emailChips(v.State, v.For)
	v.Tabs = c.emailTabs(r, "", v.For)
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
// named in the URL by its handle; the state it is narrowed to, kept in
// the URL, stays as it is.
func (c *console) findEmails(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		c.emailsShown(w, r, http.StatusUnprocessableEntity, emailsView{Problem: notAnAddress}, "")
		return
	}
	state := r.PostFormValue("state") // kept: "clear" forgets the address alone
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
