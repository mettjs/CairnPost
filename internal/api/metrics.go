package api

import (
	"bytes"
	"net/http"

	"github.com/mettjs/cairnpost/internal/metrics"
)

// metrics answers the server's numbers in the text form Prometheus
// reads. It needs an admin key, as a scraper can send one: the numbers
// say how much mail a server sends. A key bound to a sending domain is
// told of that domain's emails and endpoints only, and not how many
// requests the server answered, which would say how much others send.
func (a *app) metrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var samples []metrics.Sample
	domain := callerOf(r).key.Domain
	if a.Requests != nil && domain == "" {
		samples = a.Requests.Samples()
	}
	if a.Gauges != nil {
		samples = append(samples, a.Gauges(ctx, domain)...)
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
