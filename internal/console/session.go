package console

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
)

const (
	// cookie is the name the session's token is kept under in the browser.
	cookie = "cairnpost_console"
	// tried marks a browser that has just signed in. It is not marked
	// Secure and holds nothing: if it comes back and the session's cookie
	// does not, the browser dropped that one, and the console can say why.
	tried = "cairnpost_console_tried"
)

type whoKey struct{}

// who is the admin key a request is signed in as.
func who(r *http.Request) store.Key { return r.Context().Value(whoKey{}).(store.Key) }

// signed lets a request through only with a live session, whose key it
// then carries. The key is read anew each time: revoked, it is signed out
// at its next request.
func (c *console) signed(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		held, err := r.Cookie(cookie)
		if err != nil || held.Value == "" {
			if _, justSignedIn := r.Cookie(tried); justSignedIn == nil {
				c.drop(w, tried, false)
				c.flash(w, "unkept")
			}
			c.toSignin(w, r)
			return
		}
		key, err := c.Sessions.Key(r.Context(), held.Value)
		switch {
		case errors.Is(err, store.ErrNotFound), err == nil && key.Scope != store.ScopeAdmin:
			c.drop(w, cookie, true)
			c.flash(w, "ended")
			c.toSignin(w, r)
		case err != nil:
			c.Log.Error("console: a session could not be read", "error", err)
			c.fail(w, r, http.StatusServiceUnavailable, "The console could not reach its database. Try again shortly.")
		default:
			if _, marked := r.Cookie(tried); marked == nil {
				c.drop(w, tried, false)
			}
			next(w, r.WithContext(context.WithValue(r.Context(), whoKey{}, key)))
		}
	})
}

// toSignin sends a request with no session to the sign-in page. A change
// that was on its way is not replayed after signing in.
func (c *console) toSignin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, Root+"/signin", http.StatusSeeOther)
}

func (c *console) signinPage(w http.ResponseWriter, r *http.Request) {
	c.render(w, r, http.StatusOK, "signin", nil)
}

// signin starts a session for an admin key. The key comes in the form's
// body and goes nowhere else: not into the page answered, a log or a URL.
func (c *console) signin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	presented := strings.TrimSpace(r.PostFormValue("key"))
	key, err := c.Keys.Authenticate(r.Context(), presented)
	refuse := func(code int, why string) { c.render(w, r, code, "signin", why) }
	switch {
	case errors.Is(err, keys.ErrInvalid):
		refuse(http.StatusUnauthorized, "That is not a key CairnPost knows, or it was revoked.")
		return
	case err != nil:
		c.Log.Error("console: a sign-in could not be checked", "error", err)
		refuse(http.StatusServiceUnavailable, "The console could not reach its database. Try again shortly.")
		return
	case key.Scope != store.ScopeAdmin:
		refuse(http.StatusForbidden, "That key may only send email. Sign in with an admin key.")
		return
	}
	token, err := c.Sessions.Start(r.Context(), key)
	if err != nil {
		c.Log.Error("console: a session could not be started", "error", err)
		refuse(http.StatusServiceUnavailable, "The console could not start a session. Try again shortly.")
		return
	}
	// Who signed in is said by the key's prefix, which is public.
	c.Log.Info("console: signed in", "key", key.Prefix)
	c.keep(w, cookie, token, int(c.Sessions.TTL().Seconds()), true)
	c.keep(w, tried, "1", 60, false)
	http.Redirect(w, r, Root+"/", http.StatusSeeOther)
}

// signout ends the session here and in the browser.
func (c *console) signout(w http.ResponseWriter, r *http.Request) {
	c.endSession(w, r)
	http.Redirect(w, r, Root+"/signin", http.StatusSeeOther)
}

// endSession ends the request's session here and in the browser.
func (c *console) endSession(w http.ResponseWriter, r *http.Request) {
	if held, err := r.Cookie(cookie); err == nil {
		if err := c.Sessions.End(r.Context(), held.Value); err != nil {
			c.Log.Error("console: a session could not be ended", "error", err)
		}
	}
	c.drop(w, cookie, true)
}

// keep sets a cookie of the console's. One that is private is marked
// Secure unless the console was told it is reached over plain http.
func (c *console) keep(w http.ResponseWriter, name, value string, seconds int, private bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: Root, MaxAge: seconds,
		HttpOnly: true, Secure: private && !c.PlainHTTP, SameSite: http.SameSiteLaxMode})
}

// drop has the browser forget a cookie.
func (c *console) drop(w http.ResponseWriter, name string, private bool) {
	c.keep(w, name, "", -1, private)
}
