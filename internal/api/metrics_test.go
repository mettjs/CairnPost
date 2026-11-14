package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/metrics"
)

// The numbers are an admin's to read, count the requests answered by
// the route that matched, and never hold what a request named.
func TestMetricsAreAnAdminsAndNameNoAddress(t *testing.T) {
	r := newRigWith(t, func(d *api.Deps) {
		d.Requests = &metrics.Requests{}
		d.Gauges = func(context.Context) []metrics.Sample {
			return []metrics.Sample{{Name: "cairnpost_database_up", Help: "Whether the database answers.", Kind: metrics.Gauge, Value: 1}}
		}
	})
	if code, name, _ := r.do("GET", "/metrics", "", "", ""); code != 401 || name != "missing_api_key" {
		t.Fatalf("with no key: %d %s", code, name)
	}
	if code, name, _ := r.do("GET", "/metrics", r.send, "", ""); code != 403 || name != "admin_key_required" {
		t.Fatalf("with a sending key: %d %s", code, name)
	}
	code, _, sent := r.do("POST", "/emails", r.send, "", good)
	id, _ := sent["id"].(string)
	if code != 200 || id == "" {
		t.Fatalf("a send: %d %v", code, sent)
	}
	r.do("GET", "/emails/"+id, r.send, "", "")
	r.do("GET", "/nothing/ana@example.org", r.send, "", "")
	got := r.raw("GET", "/metrics", r.admin)
	for _, want := range []string{
		`cairnpost_http_requests_total{route="POST /emails",status="200"} 1`,
		`cairnpost_http_requests_total{route="GET /emails/{id}",status="200"} 1`,
		`cairnpost_http_requests_total{route="GET /metrics",status="403"} 1`,
		`cairnpost_http_requests_total{route="unrouted",status="404"} 1`,
		`cairnpost_http_request_seconds_count{route="POST /emails"} 1`,
		"# TYPE cairnpost_database_up gauge\ncairnpost_database_up 1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the metrics lack %q:\n%s", want, got)
		}
	}
	for _, never := range []string{id, "ana@example.org", "app@example.com", "Hello", r.admin, r.send} {
		if strings.Contains(got, never) {
			t.Errorf("the metrics hold %q", never)
		}
	}
	// Without anything to count, the route still answers.
	bare := newRig(t)
	if code, name, _ := bare.do("GET", "/metrics", bare.admin, "", ""); code != 200 || name != "" {
		t.Errorf("with nothing counted: %d %s", code, name)
	}
}
