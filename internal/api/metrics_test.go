package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/metrics"
	"github.com/mettjs/cairnpost/internal/store"
)

// The numbers are an admin's to read, count the requests answered by
// the route that matched, and never hold what a request named.
func TestMetricsAreAnAdminsAndNameNoAddress(t *testing.T) {
	var bound, asked string
	r := newRigWith(t, func(d *api.Deps) {
		d.Requests = &metrics.Requests{}
		d.Gauges = func(_ context.Context, domain string) []metrics.Sample {
			asked = domain
			return []metrics.Sample{{Name: "cairnpost_part_up", Help: "Whether a part is working.", Kind: metrics.Gauge, Value: 1}}
		}
		var err error
		if bound, _, err = d.Keys.Create(context.Background(), "tenant", store.ScopeAdmin, "example.com"); err != nil {
			t.Fatal(err)
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
		"# TYPE cairnpost_part_up gauge\ncairnpost_part_up 1\n",
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
	// A key bound to a sending domain is told of its domain alone, and
	// not how many requests the server answered for everyone.
	if got := r.raw("GET", "/metrics", bound); asked != "example.com" || !strings.Contains(got, "cairnpost_part_up 1") ||
		strings.Contains(got, "cairnpost_http_requests_total") {
		t.Errorf("for a bound key (asked of %q):\n%s", asked, got)
	}
	// Behind a mux that matches every path, as the console's is, a path
	// the API has no route for is still counted as unrouted.
	front := http.NewServeMux()
	front.Handle("/", r.h)
	req := httptest.NewRequest("GET", "/nothing/at/all", nil)
	req.Header.Set("Authorization", "Bearer "+r.admin)
	front.ServeHTTP(httptest.NewRecorder(), req)
	if got := r.raw("GET", "/metrics", r.admin); !strings.Contains(got, `cairnpost_http_requests_total{route="unrouted",status="404"} 2`) ||
		strings.Contains(got, `route="/"`) {
		t.Errorf("behind another mux:\n%s", got)
	}
	// Without anything to count, the route still answers.
	bare := newRig(t)
	if code, name, _ := bare.do("GET", "/metrics", bare.admin, "", ""); code != 200 || name != "" {
		t.Errorf("with nothing counted: %d %s", code, name)
	}
}
