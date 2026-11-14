package api

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/metrics"
)

// metrics answers the server's numbers in the text form Prometheus
// reads. It needs an admin key, as a scraper can send one: the numbers
// say how much mail a server sends.
func (a *app) metrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var samples []metrics.Sample
	if a.Requests != nil {
		samples = a.Requests.Samples()
	}
	if a.Gauges != nil {
		samples = append(samples, a.Gauges(ctx)...)
	}
	// Written whole before any of it is sent: half an exposition is none.
	var out bytes.Buffer
	if err := metrics.Write(&out, samples); err != nil {
		a.Log.Error("api: the metrics could not be written", "error", err)
		fail(w, http.StatusInternalServerError, "internal_error", "Something went wrong on our side.")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = out.WriteTo(w)
}
