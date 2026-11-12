//go:build integration

package postgres

import (
	"context"
	"strings"
)

// DumpForTest is every row of every table as text, for a test to search.
func (db *DB) DumpForTest(ctx context.Context) (string, error) {
	tables, err := db.pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		return "", err
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			return "", err
		}
		names = append(names, n)
	}
	tables.Close()
	var out strings.Builder
	for _, n := range names {
		rows, err := db.pool.Query(ctx, `SELECT to_jsonb(t)::text FROM `+n+` t`)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				return "", err
			}
			out.WriteString(n + " " + row + "\n")
		}
		rows.Close()
	}
	return out.String(), nil
}
