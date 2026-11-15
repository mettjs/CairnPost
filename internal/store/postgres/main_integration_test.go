//go:build integration

package postgres_test

import (
	"testing"

	"github.com/mettjs/cairnpost/internal/testdb"
)

// The integration tests run in a database made for them, and leave
// nothing in the one they were pointed at.
func TestMain(m *testing.M) { testdb.Main(m) }
