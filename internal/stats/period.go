package stats

import "time"

// Period is a stretch of time the numbers are shown over, cut in steps.
type Period struct {
	// Key names it in a URL; Name is how it reads after "the last".
	Key, Name string
	// Steps is how many steps it has, each Hours long: an hour or a day.
	Steps, Hours int
}

// Periods is every period, shortest first.
var Periods = []Period{{"24h", "24 hours", 24, 1}, {"7d", "7 days", 7, 24}, {"30d", "30 days", 30, 24}}

// Default is the period shown when none is asked for.
var Default = Periods[1]

// PeriodOf is the period a key names, and whether it names one.
func PeriodOf(key string) (Period, bool) {
	for _, p := range Periods {
		if p.Key == key {
			return p, true
		}
	}
	return Default, false
}

// Daily reports whether the period's steps are days.
func (p Period) Daily() bool { return p.Hours == 24 }

// back is where the step n steps before the one now is in begins: an
// hour, or a day of now's time zone, however long the clocks made it.
func (p Period) back(now time.Time, n int) time.Time {
	if p.Daily() {
		y, m, d := now.Date()
		// Where the clocks go forward at midnight there is no 00:00, and
		// the day begins at its first hour that is.
		day := time.Date(y, m, d-n, 12, 0, 0, 0, now.Location())
		start := time.Date(y, m, d-n, 0, 0, 0, 0, now.Location())
		for start.Day() != day.Day() {
			start = start.Add(time.Hour)
		}
		return start
	}
	return now.Truncate(time.Hour).Add(-time.Duration(n) * time.Hour)
}
