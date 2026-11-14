package console

import (
	"strconv"
	"time"
)

// span says a length of time in the largest unit that holds it whole:
// "5 days", "36 hours", "90 minutes".
func span(d time.Duration) string {
	for _, unit := range []struct {
		size time.Duration
		name string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}} {
		if d >= unit.size && d%unit.size == 0 {
			n := int(d / unit.size)
			if n == 1 {
				return "1 " + unit.name
			}
			return strconv.Itoa(n) + " " + unit.name + "s"
		}
	}
	return d.String()
}
