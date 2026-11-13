package console

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// shownEmails is how many emails one page of the list holds.
const shownEmails = 100

// emailsView is the list of emails and the form that narrows it.
type emailsView struct {
	Emails []store.Message
	// To and State are what the list is narrowed to, "" for not.
	To, State string
	States    []string
	// Problem says why the form was refused; Typed is what was typed.
	Problem, Typed string
	Unread         bool
	// Older is where the next page is, "" when this is the last; Paged
	// says this is not the first.
	Older string
	Paged bool
}

// found answers the address the list is narrowed to, "" for none. It is
// kept with the session and not in the page's URL, so that no proxy's
// log and no browser's history holds whom this server wrote to, and it
// ends when the session does.
func (c *console) found(r *http.Request) (string, error) {
	held, err := r.Cookie(cookie)
	if err != nil {
		return "", nil
	}
	return c.Sessions.LookingFor(r.Context(), held.Value)
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
	to, err := c.found(r)
	var list []store.Message
	if v.To = to; err == nil {
		list, err = c.Mail.List(r.Context(), who(r), mail.Query{To: v.To, State: v.State, Before: before, Limit: shownEmails + 1})
	}
	if err != nil {
		c.Log.Error("console: the emails could not be read", "error", err)
		v.Unread = true
	}
	if len(list) > shownEmails {
		list = list[:shownEmails]
		next := url.Values{"before": {list[shownEmails-1].ID}}
		if v.State != "" {
			next.Set("state", v.State)
		}
		v.Older = Root + "/emails?" + next.Encode()
	}
	v.Emails = list
	c.render(w, r, code, "emails", v)
}

// findEmails narrows the list: to an address, kept with the session, and
// to a state, kept in the URL. An empty form shows every email again.
func (c *console) findEmails(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		// More than a form of this page can hold: not read as an empty one.
		c.emailsShown(w, r, http.StatusUnprocessableEntity, emailsView{
			Problem: `Give one whole address, such as "ana@example.org".`}, "")
		return
	}
	typed, state := strings.TrimSpace(r.PostFormValue("to")), r.PostFormValue("state")
	if r.PostFormValue("clear") != "" {
		typed, state = "", ""
	}
	to := ""
	if typed != "" {
		var err error
		if to, err = mail.Recipient(typed); err != nil {
			// What was typed is shown again only if it is short enough
			// to have been an address mistyped.
			typed = strings.ToValidUTF8(typed, "")
			if len(typed) > 400 {
				typed = ""
			}
			c.emailsShown(w, r, http.StatusUnprocessableEntity, emailsView{State: state, Typed: typed,
				Problem: `Give one whole address, such as "ana@example.org".`}, "")
			return
		}
	}
	held, err := r.Cookie(cookie)
	if err == nil {
		err = c.Sessions.LookFor(r.Context(), held.Value, to)
	}
	if err != nil {
		c.Log.Error("console: what to look for could not be kept", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The list could not be narrowed. Try again shortly.")
		return
	}
	next := Root + "/emails"
	if state != "" {
		next += "?" + url.Values{"state": {state}}.Encode()
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
