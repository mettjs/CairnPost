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

// An email's files are kept only where the operator keeps what emails
// say and their files, with the words and for as long: they go when the
// words do. Kept or not, they are never logged.
func TestFilesAreKeptWithTheWordsAndGoWithThem(t *testing.T) {
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
	ks := keys.New(db)
	full, k, err := ks.Create(ctx, t.Name(), store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	kept := func(id string) (n int) {
		t.Helper()
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM message_files WHERE message_id = $1::uuid`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	domain := strings.ToLower(k.Prefix) + ".example.com"
	body := func(to, more string) string {
		return `{"from":"app@` + domain + `","to":"` + to + `-` + k.Prefix + `@example.org","subject":"s","text":"t",` + more + `
			"attachments":[{"filename":"KEPTNAMEQ.txt","content":"S0VQVEJZVEVTUQ=="}]}`
	}
	// The services' clock runs an hour behind the database's, so an
	// email asked for a minute ago is taken as still to come, and is due.
	behind := func() time.Time { return time.Now().Add(-time.Hour) }
	for name, c := range map[string]struct {
		opt  mail.Options
		want int
	}{
		"nothing kept":        {mail.Options{}, 0},
		"words without files": {mail.Options{KeepContent: time.Hour}, 0},
		"words and files":     {mail.Options{KeepContent: time.Hour, KeepFiles: true}, 1},
	} {
		c.opt.SendTimeout, c.opt.IdempotencyTTL, c.opt.InDoubtWindow, c.opt.Now = time.Second, time.Hour, time.Minute, behind
		mailer := mail.New(db, capture.New(5, time.Now), c.opt, log)
		h := api.New(api.Deps{Mail: mailer, Keys: ks, Ping: db.Ping, Driver: capture.Name, MaxBodyBytes: 1 << 20, Log: log,
			Webhooks: webhook.NewService(db, webhook.NewPoster(time.Second, true, 2, "test", time.Now), true, 20, 24*time.Hour, time.Now)})
		ask := func(method, path, body string) (*httptest.ResponseRecorder, string) {
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+full)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			var out struct{ ID string }
			json.Unmarshal(rec.Body.Bytes(), &out)
			return rec, out.ID
		}
		_, now := ask("POST", "/emails", body("now", ""))
		_, later := ask("POST", "/emails", body("later", `"scheduled_at":"`+time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)+`",`))
		runner := schedule.New(db, mailer, schedule.Options{Poll: time.Second, Batch: 1000, Parallel: 4, GiveUp: time.Hour, Abandoned: time.Hour}, time.Now, log)
		if _, _, err := runner.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		for which, id := range map[string]string{"sent at once": now, "sent later": later} {
			if id == "" || kept(id) != c.want {
				t.Fatalf("%s, %s: %d files kept, want %d", name, which, kept(id), c.want)
			}
			rec, _ := ask("GET", "/emails/"+id+"/attachments/1", "")
			if got := rec.Code == 200 && rec.Body.String() == "KEPTBYTESQ"; got != (c.want == 1) {
				t.Errorf("%s, %s: the file answers %d", name, which, rec.Code)
			}
		}
		if c.want == 0 {
			continue
		}
		// Their time up, the words are pruned and the files go with them.
		if _, err := conn.Exec(ctx, `UPDATE message_contents SET expires_at = now() - interval '1 second' WHERE message_id = ANY($1::uuid[])`,
			[]string{now, later}); err != nil {
			t.Fatal(err)
		}
		if rec, _ := ask("GET", "/emails/"+now+"/attachments/1", ""); rec.Code != 404 {
			t.Errorf("past its time, before it is pruned, the file answers %d", rec.Code)
		}
		if _, err := db.PruneContents(ctx, 100000); err != nil {
			t.Fatal(err)
		}
		if kept(now)+kept(later) != 0 {
			t.Errorf("pruned, %d files are still kept", kept(now)+kept(later))
		}
	}
	for _, mark := range []string{"KEPTNAMEQ", "KEPTBYTESQ", "S0VQVEJZVEVTUQ"} {
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
}
