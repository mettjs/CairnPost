// Package testdb gives a package's integration tests a database of
// their own: made before they run, dropped after, so they leave
// nothing in the database they were pointed at.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Env names the database server the integration tests may use. Tests
// read it for the database to work in.
const Env = "CAIRNPOST_TEST_DSN"

// prefix begins the name of every database made here.
const prefix = "cairnpost_test_"

// Main runs a package's tests in a database made for them on the
// server Env names, and drops it afterwards. A run that is killed
// leaves its database; a later run, an hour on, drops it. Env is pointed at that
// database while they run. With no Env it runs them as they are, and
// they say what they need.
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	base := os.Getenv(Env)
	if base == "" {
		return m.Run()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdb: the database server does not answer:", err)
		return 1
	}
	defer admin.Close(context.Background())
	sweep(ctx, admin)
	name := fmt.Sprintf("%s%d_%d", prefix, os.Getpid(), time.Now().UnixNano())
	scratch, err := within(base, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		return 1
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		fmt.Fprintln(os.Stderr, "testdb: a database for the tests could not be made:", err)
		return 1
	}
	defer func() {
		// The tests are done with it: anything still connected is cut off.
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			fmt.Fprintln(os.Stderr, "testdb: the tests' database could not be dropped:", err)
		}
	}()
	// The tests must not run anywhere but there: checked, not trusted.
	if in, err := current(ctx, scratch); err != nil || in != name {
		fmt.Fprintf(os.Stderr, "testdb: the tests would run in %q, not in their own database: %v\n", in, err)
		return 1
	}
	os.Setenv(Env, scratch)
	defer os.Setenv(Env, base)
	return m.Run()
}

// within is a DSN with its database changed to name.
func within(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("%s must be a postgres:// address", Env)
	}
	u.Path = "/" + name
	// A database named in the query would be used instead of the path's.
	q := u.Query()
	q.Del("dbname")
	q.Del("database")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// current is the database a DSN leads to.
func current(ctx context.Context, dsn string) (string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer conn.Close(context.Background())
	var name string
	err = conn.QueryRow(ctx, "SELECT current_database()").Scan(&name)
	return name, err
}

// made is the shape of every name made here: the prefix, the process
// and the moment, in nanoseconds.
var made = regexp.MustCompile(`^` + prefix + `[0-9]+_([0-9]{19})$`)

// sweep drops the databases a run that was killed left behind: those
// made here over an hour ago that nothing is connected to.
func sweep(ctx context.Context, admin *pgx.Conn) {
	rows, err := admin.Query(ctx, `SELECT datname FROM pg_database d
		WHERE datname LIKE $1 AND NOT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.datname = d.datname)`,
		strings.ReplaceAll(prefix, "_", `\_`)+"%")
	if err != nil {
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	for _, name := range names {
		// One just made is not connected to yet: only an old one is left
		// behind. Its name ends in when it was made.
		m := made.FindStringSubmatch(name)
		if m == nil {
			continue // not one of ours, whatever it begins with
		}
		at, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || time.Since(time.Unix(0, at)) < time.Hour {
			continue
		}
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize())
	}
}
