package testdb

import "testing"

func TestTheTestsDatabaseIsTheOnlyOneNamed(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://u:p%40ss@localhost:5425/dev?sslmode=disable": "postgres://u:p%40ss@localhost:5425/own?sslmode=disable",
		"postgresql://localhost/dev?dbname=dev&database=dev":     "postgresql://localhost/own",
		"postgres://localhost":                                   "postgres://localhost/own",
		"host=localhost dbname=dev":                              "",
		"postgresx://localhost/dev":                              "",
	} {
		if got, err := within(dsn, "own"); got != want || (err == nil) != (want != "") {
			t.Errorf("%s: %q, %v", dsn, got, err)
		}
	}
}

func TestOnlyADatabaseMadeHereIsSwept(t *testing.T) {
	for name, ours := range map[string]bool{
		"cairnpost_test_4242_1763182000000000000":  true,
		"cairnpost_test_1":                         false,
		"cairnpost_test_2025":                      false,
		"cairnpost_test_staging_20251101":          false,
		"cairnpost_test_4242_17631820000000000001": false,
		"cairnpost": false,
	} {
		if made.MatchString(name) != ours {
			t.Errorf("%s: ours %v", name, !ours)
		}
	}
}
