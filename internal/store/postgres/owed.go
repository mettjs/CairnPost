package postgres

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// givenUp is true of a delivery d of an endpoint e that is owed again
// when e is resumed: given up since the failing that paused it began.
const givenUp = `e.paused_at IS NOT NULL AND d.state = 'failed' AND d.attempted_at >= e.failing_since`

// Owed implements store.Webhooks. What waits and what was given up are
// counted apart, each from the index that holds only it, and neither
// past most.
func (db *DB) Owed(ctx context.Context, ids []string, most int) (map[string]int, error) {
	ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return !uuidShape.MatchString(id) })
	out := map[string]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.pool.Query(ctx, `
		SELECT e.id::text,
			(SELECT count(*) FROM (SELECT 1 FROM webhook_deliveries d
				WHERE d.endpoint_id = e.id AND d.state = 'pending' LIMIT $2) AS waiting) +
			(SELECT count(*) FROM (SELECT 1 FROM webhook_deliveries d
				WHERE d.endpoint_id = e.id AND `+givenUp+` LIMIT $2) AS again)
		FROM webhook_endpoints e WHERE e.id = ANY($1::uuid[])`, ids, most)
	if err != nil {
		return nil, fmt.Errorf("postgres: count what endpoints are owed: %w", err)
	}
	var (
		id string
		n  int
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &n}, func() error {
		if n > 0 {
			out[id] = min(n, most)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: count what endpoints are owed: %w", err)
	}
	return out, nil
}
