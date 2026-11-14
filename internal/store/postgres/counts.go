package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/store"
)

// SendCounts implements store.Counts. The hours are measured on the
// database's clock, which stamped the emails they count, and that time is
// answered with them: one statement, so they agree.
func (db *DB) SendCounts(ctx context.Context, domain string, hours int) ([]store.Counted, time.Time, error) {
	// Every column but the hour and the domain is a state's count: a
	// state is read by its column's name, whatever states there are.
	rows, err := db.pool.Query(ctx, `
		SELECT now(), c.hour, c.domain, to_jsonb(c) - 'hour' - 'domain'
		FROM (SELECT 1) AS always LEFT JOIN send_counts c
		  ON c.hour > date_trunc('hour', now(), 'UTC') - make_interval(hours => $2) AND ($1 = '' OR c.domain = $1)
		ORDER BY c.hour, c.domain`, domain, hours)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("postgres: read the send counts: %w", err)
	}
	var (
		out    []store.Counted
		now    time.Time
		hour   *time.Time
		name   *string
		states map[string]int
	)
	_, err = pgx.ForEachRow(rows, []any{&now, &hour, &name, &states}, func() error {
		if hour == nil || name == nil {
			return nil // no hour is counted: the time alone
		}
		c := store.Counted{Hour: hour.UTC(), Domain: *name, In: map[string]int{}}
		for state, n := range states {
			if n > 0 {
				c.In[state] = n
			}
		}
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("postgres: read the send counts: %w", err)
	}
	return out, now.UTC(), nil
}

// PruneCounts implements store.Pruning.
func (db *DB) PruneCounts(ctx context.Context, older time.Duration, limit int) (int, error) {
	tag, err := db.pool.Exec(ctx, `
		DELETE FROM send_counts WHERE (hour, domain) IN (
			SELECT hour, domain FROM send_counts WHERE hour < now() - make_interval(secs => $1)
			ORDER BY hour LIMIT $2 FOR UPDATE SKIP LOCKED)`, older.Seconds(), limit)
	if err != nil {
		return 0, fmt.Errorf("postgres: prune the send counts: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
