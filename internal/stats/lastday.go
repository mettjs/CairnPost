package stats

import (
	"context"
	"time"
)

// LastDay answers how many emails were begun in the last 24 whole
// hours, by the state they are in now: of one sending domain, or of
// every domain for "". The hour under way is left out, so the stretch
// is always a full day and the number does not fall as each hour turns.
func (s *Service) LastDay(ctx context.Context, bound string) (map[string]int, error) {
	rows, now, err := s.store.SendCounts(ctx, bound, 25)
	if err != nil {
		return nil, err
	}
	// The store's own time cuts the hours: it stamped the emails.
	until := now.UTC().Truncate(time.Hour)
	from := until.Add(-24 * time.Hour)
	in := map[string]int{}
	for _, row := range rows {
		if row.Hour.Before(from) || !row.Hour.Before(until) {
			continue
		}
		for state, n := range row.In {
			in[state] += n
		}
	}
	return in, nil
}
