// Package api is CairnPost's HTTP surface. It knows the services and
// nothing beneath them.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
