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
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/domains"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/session"
	"github.com/mettjs/cairnpost/internal/stats"
	"github.com/mettjs/cairnpost/internal/suppress"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// Root is where the console is mounted.
const Root = "/console"

// Deps is what the console is built from.
type Deps struct {
	Keys     *keys.Service
	Mail     *mail.Service
	Suppress *suppress.Service
	Webhooks *webhook.Service
	Domains  *domains.Service
	Sessions *session.Service
	// Stats reads how much was sent and what became of it.
	Stats *stats.Service
	// Quota asks the mail provider for its allowance; nil for a provider
	// that has none to tell.
	Quota func(context.Context) (Quota, error)
	// Events, Sending and Pruning report whether delivery events are read,
	// webhooks sent and old records pruned; nil for one that is off.
	Events, Sending, Pruning func() bool
	// Scheduling reports whether the emails that wait to be sent can be
	// read, and so sent; nil where none are sent from.
	Scheduling func() bool
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
	// PauseAfter is how long a webhook endpoint may fail every try
	// before it is paused, 0 for never: the pages say so.
	PauseAfter time.Duration
	// Zone is the time zone times are said in for a browser that has
	// not said its own; nil is UTC.
	Zone *time.Location
	Log  *slog.Logger
}

type console struct {
	Deps
	pages pages
	// zones are the time zones browsers have named, loaded.
	zones zones
	// assets is a word that changes when the stylesheet or the script
	// does, put in their URLs so a browser never runs old ones.
	assets string
}

// New returns the console's handler, to be mounted at Root.
func New(d Deps) http.Handler {
	if d.Zone == nil {
		d.Zone = time.UTC
	}
	c := &console{Deps: d, pages: parse(), assets: assetsVersion()}
	mux := c.routes()
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
