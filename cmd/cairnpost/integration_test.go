//go:build integration

package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
	"github.com/mettjs/cairnpost/migrations"
)

// dump is every row of every table as text. It reads the database with a
// connection of its own: a test looking at what the store wrote, not the
// store.
func dump(t *testing.T, dsn string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, name := range names {
		rows, err := conn.Query(ctx, `SELECT to_jsonb(t)::text FROM `+pgx.Identifier{name}.Sanitize()+` t`)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(strings.Join(lines, "\n") + "\n")
	}
	return out.String()
}

// Whatever is sent, and whether it is taken, refused or lost, no table and
// no log line holds the email's subject, body or header values, or the API
// key that sent it.
func TestNothingOfAnEmailsWordsIsKeptOrLogged(t *testing.T) {
	dsn := os.Getenv("CAIRNPOST_TEST_DSN")
	if dsn == "" {
		t.Fatal("set CAIRNPOST_TEST_DSN to a scratch database to run the integration tests")
	}
	ctx := context.Background()
	db, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	kept := capture.New(5, time.Now)
	ks := keys.New(db)
	full, k, err := ks.Create(ctx, t.Name(), store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	h := api.New(api.Deps{
		Mail: mail.New(db, kept, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
			InDoubtWindow: time.Minute}, log),
		Keys: ks, Ping: db.Ping, Driver: capture.Name, MaxBodyBytes: 1 << 20, Log: log,
	})
	send := func(idem, body string) int {
		req := httptest.NewRequest("POST", "/emails", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+full)
		req.Header.Set("Idempotency-Key", idem)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	const body = `{"from":"NAMEMARKQ <app@example.com>","to":"rule-one@example.org","subject":"SUBJECTMARKQ",
		"text":"TEXTMARKQ","html":"<p>HTMLMARKQ</p>","headers":{"X-Trace":"HEADERMARKQ"}}`
	if code := send("words", body); code != 200 || len(kept.Held()) != 1 {
		t.Fatalf("the send: %d", code)
	}
	// A refused request passes through validation and its error path too.
	if code := send("words-2", strings.Replace(body, `"to":"rule-one@example.org"`, `"to":"SUBJECTMARKQ"`, 1)); code != 422 {
		t.Fatalf("the refused send: %d", code)
	}
	held := dump(t, dsn)
	if !strings.Contains(held, "rule-one@example.org") || !strings.Contains(logged.String(), "POST /emails") {
		t.Fatal("the dump or the log is empty of this test's send; the check proves nothing")
	}
	secret := full[len(k.Prefix)+1:]
	for _, mark := range []string{"SUBJECTMARKQ", "TEXTMARKQ", "HTMLMARKQ", "HEADERMARKQ", "NAMEMARKQ", secret} {
		if strings.Contains(held, mark) {
			t.Errorf("the database holds %q", mark)
		}
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
}
