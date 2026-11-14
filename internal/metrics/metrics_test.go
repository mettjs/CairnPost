package metrics

import (
	"strings"
	"testing"
	"time"
)

// An exposition names each metric once, in order, and escapes what a
// label holds.
func TestAnExpositionIsWrittenAsPrometheusReadsIt(t *testing.T) {
	var r Requests
	r.Observe("POST /emails", 200, 30*time.Millisecond)
	r.Observe("POST /emails", 200, 3*time.Second)
	r.Observe("POST /emails", 422, time.Millisecond)
	r.Observe("", 404, time.Millisecond)
	var b strings.Builder
	samples := append(r.Samples(), Sample{Name: "cairnpost_build_info", Help: "The version\nrunning.", Kind: Gauge,
		Labels: [][2]string{{"version", `1.0 "x"\y`}}, Value: Up(true)})
	if err := Write(&b, samples); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"# HELP cairnpost_build_info The version\\nrunning.\n# TYPE cairnpost_build_info gauge\n",
		`cairnpost_build_info{version="1.0 \"x\"\\y"} 1` + "\n",
		"# TYPE cairnpost_http_requests_total counter\n",
		`cairnpost_http_requests_total{route="POST /emails",status="200"} 2` + "\n",
		`cairnpost_http_requests_total{route="unrouted",status="404"} 1` + "\n",
		"# TYPE cairnpost_http_request_seconds histogram\n",
		`cairnpost_http_request_seconds_bucket{route="POST /emails",le="0.005"} 1` + "\n",
		`cairnpost_http_request_seconds_bucket{route="POST /emails",le="0.1"} 2` + "\n",
		`cairnpost_http_request_seconds_bucket{route="POST /emails",le="+Inf"} 3` + "\n",
		`cairnpost_http_request_seconds_count{route="POST /emails"} 3` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the exposition lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "# TYPE cairnpost_http_request_seconds ") != 1 || strings.Count(got, "# TYPE") != 3 {
		t.Errorf("a metric is named more than once:\n%s", got)
	}
	if Up(false) != 0 {
		t.Error("no is not 0")
	}
}
