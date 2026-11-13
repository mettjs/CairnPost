package console

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/store"
)

// domainsView is the list of sending domains.
type domainsView struct {
	Domains []store.SendingDomain
	// Bound is the one sending domain the signed-in key manages, "" for all.
	Bound string
	// Problem says why a domain typed to be checked was refused; Typed
	// is what was typed.
	Problem, Typed string
	Most           int
	Unread, More   bool
}

// domainsPage lists the sending domains, and leads to the check of one
// typed into its form: a domain can be checked before anything is sent
// from it.
func (c *console) domainsPage(w http.ResponseWriter, r *http.Request) {
	v, code := domainsView{Bound: who(r).Domain, Most: domains.Most}, http.StatusOK
	if asked := strings.TrimSpace(r.URL.Query().Get("check")); asked != "" {
		if host, ok := domains.Host(asked); ok {
			http.Redirect(w, r, Root+"/domains/"+url.PathEscape(host), http.StatusSeeOther)
			return
		}
		if v.Problem, code = "That is not a domain name, such as mail.example.com.", http.StatusUnprocessableEntity; len(asked) <= 300 {
			v.Typed = strings.ToValidUTF8(asked, "")
		}
	}
	var err error
	if v.Domains, v.More, err = c.Domains.List(r.Context(), v.Bound); err != nil {
		c.Log.Error("console: the sending domains could not be read", "error", err)
		v.Unread = true
	}
	c.render(w, r, code, "domains", v)
}

// domainPage looks a domain's DNS records up and says how each stands.
// It reads public DNS, which anyone can: it changes nothing.
func (c *console) domainPage(w http.ResponseWriter, r *http.Request) {
	report, err := c.Domains.Check(r.Context(), who(r).Domain, r.PathValue("name"))
	switch {
	case errors.Is(err, domains.ErrNotAHost), errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "That is not a sending domain this key manages.")
	case err != nil:
		c.Log.Error("console: a domain could not be checked", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The domain could not be checked. Try again shortly.")
	default:
		c.render(w, r, http.StatusOK, "domain", report)
	}
}
