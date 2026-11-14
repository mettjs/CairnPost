package stats

import (
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// The last day is 24 whole hours: neither the hour under way nor the
// one that fell out of it is counted, and a bound key's domain is asked.
func TestTheLastDayIsWholeHours(t *testing.T) {
	now := time.Date(2030, 1, 2, 10, 40, 0, 0, time.UTC)
	hour := func(back int, n int) store.Counted {
		return store.Counted{Hour: now.Truncate(time.Hour).Add(-time.Duration(back) * time.Hour), Domain: "example.com",
			In: map[string]int{store.StateSent: n}}
	}
	c := &counts{now: now, rows: []store.Counted{hour(25, 1000), hour(24, 1), hour(1, 10), hour(0, 100)}}
	in, err := New(c, 0).LastDay(t.Context(), "example.com")
	if err != nil || in[store.StateSent] != 11 || c.domain != "example.com" || c.hours != 25 {
		t.Fatalf("%v %v, asked %q for %d hours", in, err, c.domain, c.hours)
	}
}
