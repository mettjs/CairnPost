package console

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mettjs/cairnpost/internal/mail"
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

// look keeps, with the session, the address a form asks to look for: its
// field "to", or none when the form asks to show everything. The lists
// of emails and of suppressions are both narrowed to it, as one is
// looked into. It answers what was typed, to show again if it was
// refused with errNotAnAddress; any other error is the store's.
func (c *console) look(r *http.Request) (typed string, err error) {
	typed = strings.TrimSpace(r.PostFormValue("to"))
	if r.PostFormValue("clear") != "" {
		typed = ""
	}
	to := ""
	if typed != "" {
		if to, err = mail.Recipient(typed); err != nil {
			// Shown again only if short enough to be an address mistyped.
			if typed = strings.ToValidUTF8(typed, ""); len(typed) > 400 {
				typed = ""
			}
			return typed, errNotAnAddress
		}
	}
	held, err := r.Cookie(cookie)
	if err != nil {
		return "", err
	}
	return "", c.Sessions.LookFor(r.Context(), held.Value, to)
}

// found answers the address the lists are narrowed to, "" for none. It is
// kept with the session and not in a page's URL, so that no proxy's log
// and no browser's history holds whom this server wrote to, and it ends
// when the session does.
func (c *console) found(r *http.Request) (string, error) {
	held, err := r.Cookie(cookie)
	if err != nil {
		return "", nil
	}
	return c.Sessions.LookingFor(r.Context(), held.Value)
}

// back sends a browser that asked for a form's own address, which a
// refused form leaves behind, to the page the form is on.
func back(page string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Root+page, http.StatusSeeOther)
	}
}
