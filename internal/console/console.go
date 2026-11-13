// Package console is the management console: pages, served by the same
// binary as the API, where whoever runs CairnPost signs in with an admin
// key and manages it. It calls the services the API calls and holds no
// rules of its own: what a key may be named, or how long a session lasts,
// is theirs to say.
//
// Everything it shows came from outside (names, notes, addresses) and is
// escaped by html/template. No page has an inline script or style, so the
// content policy it sends can forbid both.
package console

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/session"
)

// Root is where the console is mounted.
const Root = "/console"

// Deps is what the console is built from.
type Deps struct {
	Keys     *keys.Service
	Sessions *session.Service
	// Events, Sending and Pruning report whether delivery events are read,
	// webhooks sent and old records pruned; nil for one that is off.
	Events, Sending, Pruning func() bool
	// EventsOff says, where delivery events are not read, why and what
	// follows from it: that differs by driver.
	EventsOff string
	// Driver and Version are said on the overview.
	Driver, Version string
	// PlainHTTP lets the session cookie travel without TLS, for a console
	// reached over plain http in development. Otherwise the cookie is
	// marked Secure whatever the request looks like: behind a proxy that
	// ends TLS, the server sees plain http and must not go by that.
	PlainHTTP bool
	Log       *slog.Logger
}

type console struct {
	Deps
	pages pages
	// assets is a word that changes when the stylesheet or the script
	// does, put in their URLs so a browser never runs old ones.
	assets string
}

// New returns the console's handler, to be mounted at Root.
func New(d Deps) http.Handler {
	c := &console{Deps: d, pages: parse(), assets: assetsVersion()}
	mux := http.NewServeMux()
	mux.Handle("GET "+Root+"/assets/{file}", c.assetServer())
	mux.HandleFunc("GET "+Root+"/signin", c.signinPage)
	mux.HandleFunc("POST "+Root+"/signin", c.signin)
	mux.Handle("POST "+Root+"/signout", c.signed(c.signout))
	mux.Handle("GET "+Root+"/{$}", c.signed(c.overview))
	mux.Handle("GET "+Root+"/keys", c.signed(c.keysPage))
	mux.Handle("POST "+Root+"/keys", c.signed(c.createKey))
	mux.Handle("GET "+Root+"/keys/{prefix}/revoke", c.signed(c.revokePage))
	mux.Handle("POST "+Root+"/keys/{prefix}/revoke", c.signed(c.revokeKey))
	mux.HandleFunc("GET "+Root, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Root+"/", http.StatusSeeOther)
	})
	mux.Handle(Root+"/", c.signed(func(w http.ResponseWriter, r *http.Request) {
		c.fail(w, r, http.StatusNotFound, "There is no such page.")
	}))
	// A change is a POST, and a POST from another site is refused before
	// it is read: the session cookie alone never authorises one.
	guard := http.NewCrossOriginProtection()
	guard.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.fail(w, r, http.StatusForbidden, "That request came from another site and was refused.")
	}))
	return c.logged(headers(guard.Handler(mux)))
}

// headers puts on every answer what keeps a page from being framed,
// cached, or made to run anything but the console's own files.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; "+
			"form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// status remembers the status an answer was given.
type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(code int) { s.code = code; s.ResponseWriter.WriteHeader(code) }

func (s *status) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// logged writes one line per request: the route as it is declared, never
// the path or the query as they came, which can hold an address. A
// handler that panics is answered with an error, and logged as one.
func (c *console) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		began, s := time.Now(), &status{ResponseWriter: w}
		defer func() {
			if failure := recover(); failure != nil {
				// The value is not logged: it can hold what a request said.
				c.Log.Error("console: a handler panicked", "route", r.Pattern)
				if s.code == 0 {
					http.Error(s, "The page could not be shown.", http.StatusInternalServerError)
				}
			}
			if s.code == 0 {
				s.code = http.StatusOK
			}
			c.Log.Info("console: request", "method", r.Method, "route", r.Pattern, "status", s.code,
				"ms", time.Since(began).Milliseconds())
		}()
		next.ServeHTTP(s, r)
	})
}
