//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/mettjs/cairnpost/internal/schedule"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
	"github.com/mettjs/cairnpost/internal/webhook"
	"github.com/mettjs/cairnpost/migrations"
)

// An email asked for ahead of time is the one case where what it says
// and its files are kept without being asked to: only until it is sent
// or cancelled. After either, no row holds them, and no log line ever
// does.
func TestWhatAWaitingEmailSaysIsGoneOnceItIsSentOrCancelled(t *testing.T) {
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
	// The service's clock runs an hour behind the database's, so a time
	// it takes as still to come is already due.
	mailer := mail.New(db, kept, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute,
		Now: func() time.Time { return time.Now().Add(-time.Hour) }}, log)
	h := api.New(api.Deps{Mail: mailer, Keys: ks, Ping: db.Ping, Driver: capture.Name, MaxBodyBytes: 1 << 20, Log: log,
		Webhooks: webhook.NewService(db, webhook.NewPoster(time.Second, true, 2, "test", time.Now), true, 20, 24*time.Hour, time.Now)})
	domain := strings.ToLower(k.Prefix) + ".example.com"
	ask := func(path, body string) (int, string) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+full)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out struct{ ID string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.ID
	}
	body := func(to string) string {
		return `{"from":"NAMEMARKQ <app@` + domain + `>","to":"` + to + `-` + k.Prefix + `@example.org","subject":"SUBJECTMARKQ",
		"text":"TEXTMARKQ","html":"<p>HTMLMARKQ</p>","headers":{"X-Trace":"HEADERMARKQ"},
		"attachments":[{"filename":"FILEMARKQ.txt","content":"QllURVNNQVJLUQ=="}],
		"scheduled_at":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `"}`
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	held := func(id string) (words, files int) {
		t.Helper()
		if err := conn.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM scheduled_emails WHERE id = $1::uuid AND words IS NOT NULL),
			       (SELECT count(*) FROM scheduled_files WHERE email_id = $1::uuid)`, id).Scan(&words, &files); err != nil {
			t.Fatal(err)
		}
		return words, files
	}
	code, sent := ask("/emails", body("sent"))
	_, gone := ask("/emails", body("gone"))
	if code != 200 || sent == "" || gone == "" {
		t.Fatalf("asked for: %d %q %q", code, sent, gone)
	}
	// While they wait, what they say is held: the check below is of
	// something that was there.
	if w, f := held(sent); w != 1 || f != 1 {
		t.Fatalf("while it waits: %d words, %d files", w, f)
	}
	if code, _ := ask("/emails/"+gone+"/cancel", ""); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	runner := schedule.New(db, mailer, schedule.Options{Poll: time.Second, Batch: 1000, Parallel: 4, GiveUp: time.Hour, Abandoned: time.Hour}, time.Now, log)
	if _, _, err := runner.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	went := false
	for _, c := range kept.Held() {
		went = went || (c.Subject == "SUBJECTMARKQ" && len(c.Files) == 1 && c.Files[0].Size == 10)
	}
	if !went {
		t.Fatal("the email was not sent; the check proves nothing")
	}
	for name, id := range map[string]string{"sent": sent, "cancelled": gone} {
		if w, f := held(id); w != 0 || f != 0 {
			t.Errorf("once %s: %d words and %d files are still held", name, w, f)
		}
	}
	var contents int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM message_contents WHERE message_id = $1::uuid`, sent).Scan(&contents); err != nil || contents != 0 {
		t.Errorf("%d contents kept for an email nobody asked to keep: %v", contents, err)
	}
	if !strings.Contains(logged.String(), "POST /emails") {
		t.Fatal("the log is empty of this test's requests; the check proves nothing")
	}
	for _, mark := range []string{"SUBJECTMARKQ", "TEXTMARKQ", "HTMLMARKQ", "HEADERMARKQ", "NAMEMARKQ", "FILEMARKQ", "BYTESMARKQ", "QllURVNNQVJLUQ"} {
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
}
