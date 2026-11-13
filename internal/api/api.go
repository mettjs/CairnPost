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
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
)

// Deps is what the API is built from.
type Deps struct {
	Mail *mail.Service
	Keys *keys.Service
	// Ping reports whether the store answers, for GET /health.
	Ping func(context.Context) error
	// Captured answers the emails a development driver kept instead of
	// sending, newest first; nil when the driver in use delivers. With it,
	// GET /capture/emails is mounted.
	Captured func() []CapturedEmail
	// Quota answers the upstream's sending allowance; nil when the driver
	// in use has none to tell.
	Quota func(context.Context) (Quota, error)
	// Driver and Version are said by GET /health.
	Driver, Version string
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes int64
	Log          *slog.Logger
}

// New returns the handler for every route.
func New(d Deps) http.Handler {
	a := &app{Deps: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.Handle("POST /emails", a.auth(false, a.sendEmail))
	mux.Handle("GET /emails/{id}", a.auth(false, a.getEmail))
	mux.Handle("GET /limits", a.auth(false, a.limits))
	if d.Captured != nil {
		mux.Handle("GET /capture/emails", a.auth(true, a.captured))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "There is nothing at this path.")
	})
	return a.logged(mux)
}

type app struct{ Deps }

// status remembers what a handler answered, for the request log.
type status struct {
	http.ResponseWriter
	code int
}

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
		}()
		next.ServeHTTP(s, r)
	})
}

// health says whether CairnPost can do its work. It needs no key.
func (a *app) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	body := map[string]string{"status": "ok", "driver": a.Driver, "version": a.Version}
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
