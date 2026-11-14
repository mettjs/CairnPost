package metrics

import (
	"slices"
	"strconv"
	"sync"
	"time"
)

// buckets are the upper bounds, in seconds, requests are sorted by how
// long they took.
var buckets = []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Requests counts the requests a server answered: how many by route and
// status, and how long each route took. Safe for many goroutines.
type Requests struct {
	mu     sync.Mutex
	counts map[[2]string]uint64
	took   map[string]*timing
}

// timing is how long a route's requests took: how many within each
// bound, how many in all, and their sum.
type timing struct {
	within []uint64
	n      uint64
	sum    float64
}

// Observe counts one request. route is the pattern that matched, never
// a path as it was asked for: the set of routes is closed.
func (r *Requests) Observe(route string, status int, took time.Duration) {
	if route == "" {
		route = "unrouted"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts == nil {
		r.counts, r.took = map[[2]string]uint64{}, map[string]*timing{}
	}
	r.counts[[2]string{route, strconv.Itoa(status)}]++
	t := r.took[route]
	if t == nil {
		t = &timing{within: make([]uint64, len(buckets))}
		r.took[route] = t
	}
	seconds := took.Seconds()
	for i, bound := range buckets {
		if seconds <= bound {
			t.within[i]++
		}
	}
	t.n++
	t.sum += seconds
}

// Samples is what was counted, as it stands now.
func (r *Requests) Samples() []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	const (
		total, totalHelp = "cairnpost_http_requests_total", "Requests answered, by route and status."
		took, tookHelp   = "cairnpost_http_request_seconds", "How long requests took, by route."
	)
	var out []Sample
	keys := make([][2]string, 0, len(r.counts))
	for k := range r.counts {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return slices.Compare(a[:], b[:]) })
	for _, k := range keys {
		out = append(out, Sample{Name: total, Help: totalHelp, Kind: Counter,
			Labels: [][2]string{{"route", k[0]}, {"status", k[1]}}, Value: float64(r.counts[k])})
	}
	routes := make([]string, 0, len(r.took))
	for route := range r.took {
		routes = append(routes, route)
	}
	slices.Sort(routes)
	for _, route := range routes {
		t := r.took[route]
		for i, bound := range buckets {
			out = append(out, Sample{Name: took + "_bucket", Help: tookHelp, Kind: Histogram,
				Labels: [][2]string{{"route", route}, {"le", strconv.FormatFloat(bound, 'g', -1, 64)}}, Value: float64(t.within[i])})
		}
		out = append(out,
			Sample{Name: took + "_bucket", Help: tookHelp, Kind: Histogram, Labels: [][2]string{{"route", route}, {"le", "+Inf"}}, Value: float64(t.n)},
			Sample{Name: took + "_sum", Help: tookHelp, Kind: Histogram, Labels: [][2]string{{"route", route}}, Value: t.sum},
			Sample{Name: took + "_count", Help: tookHelp, Kind: Histogram, Labels: [][2]string{{"route", route}}, Value: float64(t.n)})
	}
	return out
}
