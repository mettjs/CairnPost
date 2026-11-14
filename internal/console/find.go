package console

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/session"
)

// notAnAddress is what a form is told that was asked to look for
// something that is no address.
const notAnAddress = `Give one whole address, such as "ana@example.org".`

// errNotAnAddress is a form that asked to look for no address.
var errNotAnAddress = errors.New("console: not an address")

// form reads a small form of the console's, and says whether it could:
// one larger than any of them can be is not read as an empty one.
func form(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	return r.ParseForm() == nil
}

// typed is a field of a form as it is shown again: without the space
// around it, and with nothing in it that is not text.
func typed(r *http.Request, field string) string {
	return strings.ToValidUTF8(strings.TrimSpace(r.PostFormValue(field)), "")
}

// handleOf is the name a request gives for the address its lists are
// narrowed to: "for" in a page's URL, or in the form a page sent. It
// names an address kept with the session and says nothing of it.
// One that could not be a handle is none.
func handleOf(r *http.Request) string {
	handle := r.PostFormValue("for")
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		handle = r.URL.Query().Get("for")
	}
	if !session.IsHandle(handle) {
		return ""
	}
	return handle
}

// narrowedTo adds, to a list's URL values, the handle of the address the
// list is narrowed to, if there is one.
func narrowedTo(q url.Values, handle string) url.Values {
	if handle != "" {
		q.Set("for", handle)
	}
	return q
}

// at is a page's address with its query, if it has one.
func at(page string, q url.Values) string {
	if len(q) == 0 {
		return Root + page
	}
	return Root + page + "?" + q.Encode()
}

// look keeps, with the session, the address a form asks to look for: its
// field "to", or none when the form asks to show everything. It answers
// the handle the address is kept under, "" for none, which the lists of
// emails and of suppressions are then both narrowed by; and what was
// typed, to show again if it was refused with errNotAnAddress. Any other
// error is the store's.
func (c *console) look(r *http.Request) (handle, typed string, err error) {
	typed = strings.TrimSpace(r.PostFormValue("to"))
	if r.PostFormValue("clear") != "" {
		typed = ""
	}
	if typed == "" {
		// Asked to see everything again: the address the list was
		// narrowed to is no longer kept either.
		if held, err := r.Cookie(cookie); err == nil {
			return "", "", c.Sessions.Forget(r.Context(), held.Value, handleOf(r))
		}
		return "", "", nil
	}
	to, err := mail.Recipient(typed)
	if err != nil {
		// Shown again only if short enough to be an address mistyped.
		if typed = strings.ToValidUTF8(typed, ""); len(typed) > 400 {
			typed = ""
		}
		return "", typed, errNotAnAddress
	}
	held, err := r.Cookie(cookie)
	if err != nil {
		return "", "", err
	}
	handle, err = c.Sessions.LookFor(r.Context(), held.Value, to)
	return handle, "", err
}

// found answers the address a request's lists are narrowed to, and the
// handle that names it; both "" for none. The address is kept with the
// session and only its handle is in a page's URL, so that no proxy's log
// and no browser's history holds whom this server wrote to; it ends when
// the session does. Each page carries its own handle, so two tabs of one
// sign-in each keep their own narrowing.
func (c *console) found(r *http.Request) (address, handle string, err error) {
	held, err := r.Cookie(cookie)
	if err != nil {
		return "", "", nil
	}
	handle = handleOf(r)
	if address, err = c.Sessions.LookingFor(r.Context(), held.Value, handle); address == "" {
		handle = "" // one this session does not keep narrows nothing
	}
	return address, handle, err
}

// forgotten reports whether a request names an address that the session
// no longer keeps: its list is then whole, and the page says why.
func forgotten(r *http.Request, handle string) bool { return handleOf(r) != "" && handle == "" }

// back sends a browser that asked for a form's own address, which a
// refused form leaves behind, to the page the form is on, narrowed as
// the form's address says it was.
func back(page string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, at(page, narrowedTo(url.Values{}, handleOf(r))), http.StatusSeeOther)
	}
}
