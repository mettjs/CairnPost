// Package api is CairnPost's HTTP surface. It knows the services and
// nothing beneath them.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/metrics"
	"github.com/mettjs/cairnpost/internal/suppress"
	"github.com/mettjs/cairnpost/internal/templates"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// Deps is what the API is built from.
type Deps struct {
	Mail     *mail.Service
	Keys     *keys.Service
	Suppress *suppress.Service
	Webhooks *webhook.Service
	// Inbound takes the emails that come and reads them.
	Inbound *inbound.Service
	// Templates are the emails written once to be sent many times.
	Templates *templates.Service
	// Ping reports whether the store answers, for GET /health.
	Ping func(context.Context) error
	// Captured answers the emails a development driver kept instead of
	// sending, newest first; nil when the driver in use delivers. With it,
	// GET /capture/emails is mounted.
	Captured func() []CapturedEmail
	// Quota answers the upstream's sending allowance; nil when the driver
	// in use has none to tell.
	Quota func(context.Context) (Quota, error)
	// Events reports whether delivery events are being read; nil when the
	// driver in use has none to read.
	Events func() bool
	// Sending reports whether what the webhook endpoints are owed can be
	// read, and so sent; nil when no webhooks are sent from here.
	Sending func() bool
	// Pruning reports whether the last pass of the pruner finished; nil
	// when nothing is pruned.
	Pruning func() bool
	// Scheduling reports whether the emails that wait to be sent can
	// be read, and so sent; nil when none are sent from here.
	Scheduling func() bool
	// Requests counts what the API answers, and Gauges answers how the
	// server stands in numbers, both for GET /metrics; nil for neither.
	Requests *metrics.Requests
	Gauges   func(ctx context.Context, domain string) []metrics.Sample
	// Driver and Version are said by GET /health.
	Driver, Version string
	// MaxBodyBytes bounds a request body. MaxEmailBytes bounds that of
	// a send, which carries its attachments; 0 is MaxBodyBytes.
	MaxBodyBytes  int64
	MaxEmailBytes int64
	// MaxUploadBytes bounds the sends larger than MaxBodyBytes that are
	// being taken at one time, by the sum of their bodies; 0 is no bound.
	MaxUploadBytes int64
	// BatchTime is how long a batch may take to answer, from the moment
	// it is read; 0 leaves the server's own bound.
	BatchTime time.Duration
	Log       *slog.Logger
}

// New returns the handler for every route.
func New(d Deps) http.Handler {
	a := &app{Deps: d}
	mux := http.NewServeMux()
	for _, r := range routes {
		if r.when != nil && !r.when(d) {
			continue
		}
		serve := r.serve(a)
		if r.key == KeyNone {
			mux.Handle(r.pattern, serve)
			continue
		}
		mux.Handle(r.pattern, a.auth(r.key == KeyAdmin, serve))
	}
	return a.logged(unrouted(mux))
}

// unrouted answers, in the API's own form, a request no route takes. A
// path that exists under another method says which methods it takes. A
// path that does not exist is named apart from a record that is not
// there: a caller whose base address is wrong must not read it as "no
// such email".
func unrouted(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		// No route of this API took it, whatever a mux in front of the
		// API matched: the request is logged and counted as unrouted.
		r.Pattern = ""
		// The mux knows which of the two it is, and says so in plain
		// text: its answer is read, not sent.
		probe := &unsent{header: http.Header{}}
		h.ServeHTTP(probe, r)
		if probe.code == http.StatusMethodNotAllowed {
			allow := probe.header.Get("Allow")
			w.Header().Set("Allow", allow)
			fail(w, http.StatusMethodNotAllowed, "method_not_allowed", "This path does not take "+r.Method+". It takes: "+allow+".")
			return
		}
		fail(w, http.StatusNotFound, "route_not_found", "There is nothing at this path. Check the path and the server's address.")
	})
}

// unsent takes an answer that is only looked at.
type unsent struct {
	header http.Header
	code   int
}

func (u *unsent) Header() http.Header         { return u.header }
func (u *unsent) Write(b []byte) (int, error) { return len(b), nil }
func (u *unsent) WriteHeader(code int) {
	if u.code == 0 {
		u.code = code
	}
}

type app struct {
	Deps
	uploads uploads
}

// status remembers what a handler answered, for the request log.
type status struct {
	http.ResponseWriter
	code int
}

// Unwrap lets a handler reach the connection's own controls.
func (s *status) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *status) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// logged writes one line per request: the route as it was matched, never a
// path's own values, a query, a header or anything of the body.
func (a *app) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		began := time.Now()
		s := &status{ResponseWriter: w, code: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				a.Log.Error("api: a handler panicked", "route", r.Pattern, "panic", p)
				fail(s, http.StatusInternalServerError, "internal_error", "Something went wrong on our side.")
			}
			a.Log.Info("api: request", "method", r.Method, "route", r.Pattern, "status", s.code,
				"ms", time.Since(began).Milliseconds())
			if a.Requests != nil {
				a.Requests.Observe(r.Pattern, s.code, time.Since(began))
			}
		}()
		next.ServeHTTP(s, r)
	})
}

// health says whether CairnPost can do its work. It needs no key.
func (a *app) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	body := map[string]string{"status": "ok", "driver": a.Driver, "version": a.Version, "events": "off", "webhooks": "off", "pruning": "off", "scheduled": "off", "inbound": "off"}
	if a.Inbound != nil && a.Inbound.On() {
		body["inbound"] = "on"
	}
	if a.Sending != nil {
		body["webhooks"] = "sending"
		if !a.Sending() {
			body["webhooks"] = "failing"
		}
	}
	if a.Scheduling != nil {
		body["scheduled"] = "sending"
		if !a.Scheduling() {
			body["scheduled"] = "failing"
		}
	}
	if a.Pruning != nil {
		body["pruning"] = "running"
		if !a.Pruning() {
			body["pruning"] = "failing"
		}
	}
	// Whether delivery events are being read is said and does not change
	// the status: mail still goes while they are not.
	if a.Events != nil {
		body["events"] = "reading"
		if !a.Events() {
			body["events"] = "failing"
		}
	}
	if err := a.Ping(ctx); err != nil {
		a.Log.Error("api: the database does not answer", "error", err)
		body["status"] = "unavailable"
		write(w, http.StatusServiceUnavailable, body)
		return
	}
	write(w, http.StatusOK, body)
}

// Quota is the mail provider's sending allowance and how much is used.
type Quota struct {
	Limit    int        `json:"limit"`
	Per      string     `json:"per"`
	Used     int        `json:"used"`
	Over     bool       `json:"over"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// limits is GET /limits: how much the mail provider will still send, so a
// caller can know before it runs out. "quota" is null for a provider that
// has no allowance to tell. It needs a key: how much an installation sends
// is not for strangers.
func (a *app) limits(w http.ResponseWriter, r *http.Request) {
	if a.Quota == nil {
		write(w, http.StatusOK, map[string]any{"driver": a.Driver, "quota": nil})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	q, err := a.Quota(ctx)
	var throttled *driver.Throttled
	switch {
	case err == nil:
		write(w, http.StatusOK, map[string]any{"driver": a.Driver, "quota": q})
	case r.Context().Err() != nil:
		// The caller left; there is nobody to answer and nothing wrong.
	case errors.As(err, &throttled):
		if throttled.After > 0 {
			w.Header().Set("Retry-After", seconds(throttled.After))
		}
		fail(w, http.StatusTooManyRequests, "rate_limit_exceeded",
			"The mail provider is being asked too often. Try again later.")
	case errors.Is(err, driver.ErrUnavailable):
		a.Log.Warn("api: the provider refused to say its limits", "error", err)
		fail(w, http.StatusServiceUnavailable, "upstream_unavailable",
			"The mail provider refused to say its limits. If this persists, check the driver's credentials; the server's log has the provider's answer.")
	default:
		a.Log.Warn("api: the provider's limits could not be read", "error", err)
		fail(w, http.StatusBadGateway, "upstream_unknown",
			"The mail provider did not answer about its limits. Try again shortly.")
	}
}
