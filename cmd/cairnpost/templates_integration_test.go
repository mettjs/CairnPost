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

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
	"github.com/mettjs/cairnpost/internal/templates"
	"github.com/mettjs/cairnpost/internal/webhook"
	"github.com/mettjs/cairnpost/migrations"
)

// A template is kept in its own table and nowhere else. What a send
// gives its variables, and the email filled from it, are an email's
// words: with nothing set to keep them, no table and no log line holds
// them, whether the send went or was refused.
func TestATemplatesValuesAreKeptAndLoggedNowhere(t *testing.T) {
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
	written := templates.NewService(db, templates.Options{Max: 10, MaxBytes: 4096})
	held := capture.New(5, time.Now)
	mailer := mail.New(db, held, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute, Templates: written}, log)
	h := api.New(api.Deps{Mail: mailer, Keys: ks, Templates: written, Ping: db.Ping, Driver: capture.Name, MaxBodyBytes: 1 << 20, Log: log,
		Webhooks: webhook.NewService(db, webhook.NewPoster(time.Second, true, 2, "test", time.Now), true, 20, 24*time.Hour, time.Now)})
	ask := func(method, path, idem, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+full)
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	domain := strings.ReplaceAll(strings.ToLower(k.Prefix), "_", "-") + ".example.com"
	code, made := ask("POST", "/templates", "", `{"name":"mark","domain":"`+domain+`","subject":"TPLSUBJECTQ {{who}}",
		"html":"<p>TPLHTMLQ {{code}}</p>","text":"TPLTEXTQ {{code}}","defaults":{"who":"TPLDEFAULTQ"}}`)
	if code != 201 {
		t.Fatalf("the template: %d %v", code, made)
	}
	t.Cleanup(func() { db.DeleteTemplate(ctx, made["id"].(string)) })
	send := func(idem, variables string) (int, map[string]any) {
		return ask("POST", "/emails", idem, `{"from":"app@`+domain+`","to":"filled-`+k.Prefix+`@example.org","template":{"name":"mark","variables":{`+variables+`}}}`)
	}
	if code, out := send("tpl-1", `"code":"VALUEMARKQ"`); code != 200 {
		t.Fatalf("the send: %d %v", code, out)
	}
	// The same again is the same email; another value is another request.
	if code, _ := send("tpl-1", `"code":"VALUEMARKQ"`); code != 200 || len(held.Held()) != 1 {
		t.Fatalf("the repeat: %d, %d emails", code, len(held.Held()))
	}
	if code, _ := send("tpl-1", `"code":"OTHERMARKQ"`); code != 409 {
		t.Fatalf("another value under the same key: %d", code)
	}
	// Refused: a value for no variable, and a variable with none.
	for _, variables := range []string{`"code":"VALUEMARKQ","WRONGNAMEQ":"VALUEMARKQ"`, `"who":"VALUEMARKQ"`} {
		if code, out := send("", variables); code != 422 || strings.Contains(out["message"].(string), "VALUEMARKQ") {
			t.Fatalf("a refused send: %d %v", code, out)
		}
	}
	for _, m := range held.Held() {
		if m.Subject != "TPLSUBJECTQ TPLDEFAULTQ" || m.HTML != "<p>TPLHTMLQ VALUEMARKQ</p>" || m.Text != "TPLTEXTQ VALUEMARKQ" {
			t.Fatalf("the email was filled as %+v", m)
		}
	}
	// The template gone, what was asked before under a key is answered
	// as it was, alone and in a batch, and nothing new is.
	batch := `[{"from":"app@` + domain + `","to":"batch-` + k.Prefix + `@example.org","template":{"name":"mark","variables":{"code":"VALUEMARKQ"}}}]`
	if code, out := ask("POST", "/emails/batch", "tpl-b", batch); code != 200 {
		t.Fatalf("the batch: %d %v", code, out)
	}
	everywhere, elsewhere := dump(t, dsn), dump(t, dsn, "templates")
	if code, _ := ask("DELETE", "/templates/"+made["id"].(string), "", ""); code != 204 {
		t.Fatalf("the template deleted: %d", code)
	}
	if code, out := send("tpl-1", `"code":"VALUEMARKQ"`); code != 200 {
		t.Errorf("the repeat, the template gone: %d %v", code, out)
	}
	if code, out := ask("POST", "/emails/batch", "tpl-b", batch); code != 200 {
		t.Errorf("the batch again, the template gone: %d %v", code, out)
	}
	for _, idem := range []string{"", "tpl-new"} {
		if code, _ := send(idem, `"code":"VALUEMARKQ"`); code != 422 {
			t.Errorf("a new request, the template gone (key %q): %d", idem, code)
		}
	}
	if code, _ := ask("POST", "/emails/batch", "tpl-b2", batch); code != 422 {
		t.Errorf("a new batch, the template gone: %d", code)
	}
	if n := len(held.Held()); n != 2 {
		t.Errorf("%d emails went", n)
	}
	if !strings.Contains(everywhere, "TPLSUBJECTQ") || !strings.Contains(elsewhere, k.ID) {
		t.Fatal("the dump cannot read the template, or is empty of this test; the check proves nothing")
	}
	for _, mark := range []string{"VALUEMARKQ", "OTHERMARKQ", "TPLSUBJECTQ", "TPLHTMLQ", "TPLTEXTQ", "TPLDEFAULTQ"} {
		if strings.Contains(elsewhere, mark) {
			t.Errorf("a table that is not the templates' holds %q", mark)
		}
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
	if strings.Contains(everywhere, "VALUEMARKQ") {
		t.Error("the database holds a value given to a template")
	}
}
