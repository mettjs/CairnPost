package api

import (
	"math"
	"strconv"
	"testing"
	"time"
)

// A wait is written in whole seconds, rounded up, never less than one
// and never more than the longest a client reads: what the mail
// provider says to wait is not bounded, and a wait in ten digits is one
// no client reads at all.
func TestAWaitIsWrittenInWholeSecondsUpToTheLongest(t *testing.T) {
	for d, want := range map[time.Duration]string{
		math.MinInt64:                   "1",
		-time.Hour:                      "1",
		0:                               "1",
		time.Nanosecond:                 "1",
		time.Second:                     "1",
		1200 * time.Millisecond:         "2",
		30 * 24 * time.Hour:             "2592000",
		longestWait * time.Second:       "999999999",
		(longestWait + 1) * time.Second: "999999999",
		40 * 365 * 24 * time.Hour:       "999999999",
		math.MaxInt64:                   "999999999",
	} {
		if got := seconds(d); got != want {
			t.Errorf("a wait of %v is written %q, not %q", d, got, want)
		}
	}
	if _, err := strconv.Atoi(seconds(math.MaxInt64)); err != nil {
		t.Error(err)
	}
}
