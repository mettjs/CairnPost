package console

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
)

// keysView is the keys page: the list, and the form to make one.
type keysView struct {
	Keys []store.Key
	// Bound is the one sending domain the signed-in key manages, "" for all.
	Bound string
	// Made is a key just made, shown in full this once; nil otherwise.
	Made     *store.Key
	MadeFull string
	// Problem says why the form was refused; Form is what was typed, so
	// it is not typed again. Neither holds a key.
	Problem string
	Form    keyForm
	// Unread says the list could not be read; More that it is longer
	// than the page shows.
	Unread, More bool
}

type keyForm struct{ Name, Scope, Domain string }

// shownKeys is how many keys the page lists: the newest. A server with
// more has them all on the command line.
const shownKeys = 500

// keysShown is the page with the keys the signed-in key may see, newest
// first.
func (c *console) keysShown(w http.ResponseWriter, r *http.Request, code int, v keysView) {
	me := who(r)
	v.Bound = me.Domain
	list, err := c.Keys.ListFor(r.Context(), me.Domain, shownKeys+1)
	if err != nil {
		c.Log.Error("console: the keys could not be read", "error", err)
		v.Unread = true
	}
	if len(list) > shownKeys {
		list, v.More = list[:shownKeys], true
	}
	v.Keys = list
	if v.Form.Scope == "" {
		v.Form.Scope = store.ScopeSend
	}
	c.render(w, r, code, "keys", v)
}

func (c *console) keysPage(w http.ResponseWriter, r *http.Request) {
	c.keysShown(w, r, http.StatusOK, keysView{})
}

// createKey makes a key and shows it, in the answer to the request that
// made it and never again. What a key may be named is the keys service's
// to say; its sentence is passed on.
func (c *console) createKey(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	form := keyForm{Name: typed(r, "name"), Scope: r.PostFormValue("scope"), Domain: typed(r, "domain")}
	refuse := func(code int, why string) { c.keysShown(w, r, code, keysView{Problem: why, Form: form}) }
	me := who(r)
	full, made, err := c.Keys.MakeFor(r.Context(), me.Domain, keys.Request{Name: r.PostFormValue("name"), Scope: form.Scope,
		Domain: r.PostFormValue("domain"), By: me.Prefix})
	var invalid *keys.Invalid
	switch {
	case errors.As(err, &invalid):
		refuse(http.StatusUnprocessableEntity, sentence(invalid.Reason))
	case errors.Is(err, keys.ErrDomain):
		refuse(http.StatusForbidden, "The key you are signed in with manages one sending domain, and can only make keys for it.")
	case errors.Is(err, keys.ErrLimit):
		refuse(http.StatusConflict, tooMany(r.PostFormValue("domain")))
	case err != nil:
		c.Log.Error("console: a key could not be made", "error", err)
		refuse(http.StatusServiceUnavailable, "The key could not be made. Try again shortly.")
	default:
		// Prefixes are public: this is the record of who made what.
		c.Log.Info("console: a key was made", "by", me.Prefix, "key", made.Prefix, "scope", made.Scope)
		c.keysShown(w, r, http.StatusCreated, keysView{Made: &made, MadeFull: full})
	}
}

// sentence is a reason as a page says it: capitalised, with a full stop.
func sentence(reason string) string {
	if reason == "" {
		return reason
	}
	return strings.ToUpper(reason[:1]) + reason[1:] + "."
}

// revokePage asks before a key is revoked: it cannot be undone.
func (c *console) revokePage(w http.ResponseWriter, r *http.Request) {
	key, err := c.Keys.Find(r.Context(), who(r).Domain, r.PathValue("prefix"))
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && key.RevokedAt != nil:
		c.fail(w, r, http.StatusNotFound, "There is no such key, or it is already revoked.")
	case err != nil:
		c.Log.Error("console: a key could not be read", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The key could not be read. Try again shortly.")
	default:
		c.render(w, r, http.StatusOK, "revoke", struct {
			Key  store.Key
			Mine bool
		}{key, key.ID == who(r).ID})
	}
}

func (c *console) revokeKey(w http.ResponseWriter, r *http.Request) {
	err := c.Keys.RevokeFor(r.Context(), who(r).Domain, r.PathValue("prefix"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such key, or it is already revoked.")
	case err != nil:
		c.Log.Error("console: a key could not be revoked", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The key could not be revoked. Try again shortly.")
	case r.PathValue("prefix") == who(r).Prefix:
		// The key signed in with: its session is over, and is said to be.
		c.Log.Info("console: a key was revoked", "by", who(r).Prefix, "key", r.PathValue("prefix"))
		c.endSession(w, r)
		c.flash(w, "revoked-own")
		http.Redirect(w, r, Root+"/signin", http.StatusSeeOther)
	default:
		c.Log.Info("console: a key was revoked", "by", who(r).Prefix, "key", r.PathValue("prefix"))
		c.flash(w, "revoked")
		http.Redirect(w, r, Root+"/keys", http.StatusSeeOther)
	}
}

// tooMany says a key was not made because there are as many as may be,
// in words that fit a key of one sending domain and a key of none.
func tooMany(domain string) string {
	of := "that send from any domain"
	if strings.TrimSpace(domain) != "" {
		of = "for this sending domain"
	}
	return "There are as many keys " + of + " as this server allows (CAIRNPOST_MAX_KEYS). Revoke one that is no longer used."
}
