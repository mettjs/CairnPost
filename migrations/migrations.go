// Package migrations holds the schema as versioned SQL files, embedded in
// the binary so a release carries its own schema.
package migrations

import "embed"

// FS is every migration, named NNNNN_what.sql and applied in name order.
//
//go:embed *.sql
var FS embed.FS
