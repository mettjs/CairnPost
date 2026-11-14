package memstore

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// bucket is one hour of one sending domain.
type bucket struct {
	hour   time.Time
	domain string
}

// count moves a message from one state's count to another's, under the
// hour it was sent; from is "" for a message that is new. A message that
// is deleted stays counted, and one whose hour's count is already pruned
// is not counted again.
func (s *Store) count(m store.Message, from, to string) {
	if from == to {
		return
	}
	at := bucket{m.CreatedAt.UTC().Truncate(time.Hour), store.DomainOf(m.Sender)}
	if s.counts[at] == nil {
		if from != "" {
			return // its hour's count was pruned: the move is not counted
		}
		s.counts[at] = map[string]int{}
	}
	if s.counts[at][from] > 0 {
		s.counts[at][from]--
	}
	s.counts[at][to]++
}

// SendCounts implements store.Counts.
func (s *Store) SendCounts(_ context.Context, domain string, hours int) ([]store.Counted, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().UTC()
	first := now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)
	var out []store.Counted
	for at, in := range s.counts {
		if at.hour.Before(first) || (domain != "" && at.domain != domain) {
			continue
		}
		c := store.Counted{Hour: at.hour, Domain: at.domain, In: maps.Clone(in)}
		maps.DeleteFunc(c.In, func(_ string, n int) bool { return n == 0 })
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b store.Counted) int {
		return cmp.Or(a.Hour.Compare(b.Hour), cmp.Compare(a.Domain, b.Domain))
	})
	return out, now, nil
}

// PruneCounts implements store.Pruning.
func (s *Store) PruneCounts(_ context.Context, older time.Duration, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, before := 0, s.Now().Add(-older)
	for at := range s.counts {
		if n < limit && at.hour.Before(before) {
			delete(s.counts, at)
			n++
		}
	}
	return n, nil
}
