//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/driver/cloudflare"
	"github.com/mettjs/cairnpost/internal/events"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
	"github.com/mettjs/cairnpost/internal/webhook"
	"github.com/mettjs/cairnpost/migrations"
)

// dump is every row of every table as text, but for the tables named. It reads the database with a
// connection of its own: a test looking at what the store wrote, not the
// store.
func dump(t *testing.T, dsn string, but ...string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// Bytes are printed as the text they are, not as hex: the tables that
	// may hold an email's words keep them as bytes, and a mark must be
	// findable there.
	if _, err := conn.Exec(ctx, `SET bytea_output = 'escape'`); err != nil {
		t.Fatal(err)
	}
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
		if slices.Contains(but, name) {
			continue
		}
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
	poster := webhook.NewPoster(5*time.Second, true, 2, "test", time.Now)
	ks := keys.New(db)
	full, k, err := ks.Create(ctx, t.Name(), store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	h := api.New(api.Deps{
		Mail: mail.New(db, kept, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
			InDoubtWindow: time.Minute}, log),
		Keys: ks, Ping: db.Ping, Driver: capture.Name, MaxBodyBytes: 1 << 20, Log: log,
		Webhooks: webhook.NewService(db, poster, true, 20, 24*time.Hour, time.Now),
	})
	// An application's endpoint, told what becomes of this run's emails.
	var hooks []*http.Request
	var hookBodies []string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hooks, hookBodies = append(hooks, r), append(hookBodies, string(b))
	}))
	defer app.Close()
	// A sending domain of this run's own: the database outlives the run,
	// and the endpoint must hear about no other test's emails.
	domain := strings.ToLower(k.Prefix) + ".example.com"
	req := httptest.NewRequest("POST", "/webhooks", strings.NewReader(`{"url":"`+app.URL+`/HOOKPATHMARKQ","domain":"`+domain+`"}`))
	req.Header.Set("Authorization", "Bearer "+full)
	made := httptest.NewRecorder()
	h.ServeHTTP(made, req)
	var endpoint struct{ ID, Secret string }
	if json.Unmarshal(made.Body.Bytes(), &endpoint); made.Code != 201 || !strings.HasPrefix(endpoint.Secret, "whsec_") {
		t.Fatalf("the endpoint: %d %s", made.Code, made.Body)
	}
	defer db.DeleteEndpoint(ctx, endpoint.ID)
	send := func(idem, body string) int {
		req := httptest.NewRequest("POST", "/emails", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+full)
		req.Header.Set("Idempotency-Key", idem)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// An address of this run's own: the bounce below suppresses it, and the
	// database outlives the run.
	to := "rule-one-" + k.Prefix + "@example.org"
	body := `{"from":"NAMEMARKQ <app@` + domain + `>","to":"` + to + `","subject":"SUBJECTMARKQ",
		"text":"TEXTMARKQ","html":"<p>HTMLMARKQ</p>","headers":{"X-Trace":"HEADERMARKQ"},
		"attachments":[{"filename":"FILEMARKQ.txt","content":"QllURVNNQVJLUQ=="}]}`
	if code := send("words", body); code != 200 || len(kept.Held()) != 1 {
		t.Fatalf("the send: %d", code)
	}
	// A refused request passes through validation and its error path too.
	if code := send("words-2", strings.Replace(body, `"to":"`+to+`"`, `"to":"SUBJECTMARKQ"`, 1)); code != 422 {
		t.Fatalf("the refused send: %d", code)
	}
	// An upstream's event carries the subject too, and a receiving server's
	// words. The event is read by the real Cloudflare reader and recorded.
	var upstreamID string
	for _, c := range kept.Held() {
		upstreamID = c.ID
	}
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ack") {
			io.WriteString(w, `{"success":true,"result":{}}`)
			return
		}
		event := `{"type":"cf.email.sending.message.bounced","metadata":{"eventTimestamp":"2026-01-02T03:04:05Z"},` +
			`"payload":{"eventId":"rule-one-` + upstreamID + `","messageId":"` + upstreamID + `","recipient":"` + to + `",` +
			`"subject":"SUBJECTMARKQ","delivery":{"smtpResponse":"DETAILMARKQ"},` +
			`"bounce":{"type":"hard","classification":"permanent_failure","reason":"DETAILMARKQ SUBJECTMARKQ"}}}`
		body, _ := json.Marshal(map[string]any{"success": true, "result": map[string]any{
			"messages": []map[string]any{{"body": event, "lease_id": "l1"}}}})
		w.Write(body)
	}))
	defer queue.Close()
	source := cloudflare.NewAt(queue.URL, "token").Events("q")
	reader := events.New(db, db.Ping, source, capture.Name, events.Options{Poll: time.Second, Batch: 10,
		Lease: time.Minute, Retry: time.Second, Grace: time.Minute}, time.Now, log)
	if n, err := reader.Pass(ctx); n != 1 || err != nil {
		t.Fatalf("the event: %d %v", n, err)
	}
	// The event is owed to the endpoint, and paid: signed, and with nothing
	// of the email's words.
	worker := webhook.NewWorker(db, poster, webhook.Options{Poll: 50 * time.Millisecond, Workers: 4,
		PerEndpoint: 2, Retries: []time.Duration{time.Minute}}, log)
	if _, err := worker.Pass(ctx); err != nil || len(hooks) != 1 {
		t.Fatalf("the webhook: %d received, %v", len(hooks), err)
	}
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(endpoint.Secret, "whsec_"))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(hooks[0].Header.Get("svix-id") + "." + hooks[0].Header.Get("svix-timestamp") + "." + hookBodies[0]))
	if hooks[0].Header.Get("svix-signature") != "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatal("the webhook's signature does not check with the secret the endpoint was given")
	}
	if !strings.Contains(hookBodies[0], `"type":"email.bounced"`) || !strings.Contains(hookBodies[0], `"bounce":{"type":"Permanent"}`) ||
		!strings.Contains(hookBodies[0], to) || strings.Contains(hookBodies[0], "MARKQ") {
		t.Fatalf("the webhook: %s", hookBodies[0])
	}
	// Neither the signing secret nor the endpoint's URL, which can hold a
	// token, is logged; and the secret is not said again.
	list := httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/webhooks/"+endpoint.ID, nil)
	req.Header.Set("Authorization", "Bearer "+full)
	h.ServeHTTP(list, req)
	for _, mark := range []string{endpoint.Secret, strings.TrimPrefix(endpoint.Secret, "whsec_"), "HOOKPATHMARKQ", app.URL} {
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
	if list.Code != 200 || strings.Contains(list.Body.String(), "whsec_") {
		t.Errorf("the endpoint, read again, says its secret: %d %s", list.Code, list.Body)
	}
	// A batch goes the same way, taken and refused.
	batch := func(idem, body string) int {
		req := httptest.NewRequest("POST", "/emails/batch", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+full)
		req.Header.Set("Idempotency-Key", idem)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	second := strings.Replace(body, `"to":"`+to+`"`, `"to":"rule-one-b-`+k.Prefix+`@example.org"`, 1)
	if code := batch("words-3", "["+second+","+strings.Replace(second, "rule-one-b-", "rule-one-c-", 1)+"]"); code != 200 {
		t.Fatalf("the batch: %d", code)
	}
	if code := batch("words-4", "["+second+","+strings.Replace(body, `"to":"`+to+`"`, `"to":"SUBJECTMARKQ"`, 1)+"]"); code != 422 {
		t.Fatalf("the refused batch: %d", code)
	}
	held := dump(t, dsn)
	// The event was recorded, by what Cloudflare called it; the receiving
	// server's own sentence, which here quotes the subject, was not.
	if !strings.Contains(held, "permanent_failure") {
		t.Fatal("the event did not reach the tables; the check proves nothing")
	}
	if !strings.Contains(held, to) || !strings.Contains(logged.String(), "POST /emails") {
		t.Fatal("the dump or the log is empty of this test's send; the check proves nothing")
	}
	// Nothing asked for the email's words to be kept, and none were.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var sentRows, keptRows int
	if err := conn.QueryRow(ctx, `
		SELECT count(*), count(c.message_id) FROM messages m LEFT JOIN message_contents c ON c.message_id = m.id
		WHERE m.api_key_id = $1::uuid`, k.ID).Scan(&sentRows, &keptRows); err != nil || sentRows == 0 || keptRows != 0 {
		t.Errorf("%d emails on record, the words of %d kept: %v", sentRows, keptRows, err)
	}
	secret := full[len(k.Prefix)+1:]
	for _, mark := range []string{"SUBJECTMARKQ", "TEXTMARKQ", "HTMLMARKQ", "HEADERMARKQ", "NAMEMARKQ", "DETAILMARKQ",
		"FILEMARKQ", "BYTESMARKQ", "QllURVNNQVJLUQ", secret} {
		if strings.Contains(held, mark) {
			t.Errorf("the database holds %q", mark)
		}
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
}

// With content retention on, an email's words are in one table and in the
// answer to an admin key, and nowhere else: no other table, no log line,
// not the answer to the key that sent it. Past their time they are not
// shown, and with the setting off again they are hidden at once.
func TestWhenWordsAreKeptTheyAreKeptInOnePlace(t *testing.T) {
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
	admin, _, err := ks.Create(ctx, t.Name()+" admin", store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	sender, _, err := ks.Create(ctx, t.Name()+" sender", store.ScopeSend, "")
	if err != nil {
		t.Fatal(err)
	}
	serving := func(keep time.Duration) http.Handler {
		return api.New(api.Deps{
			Mail: mail.New(db, capture.New(5, time.Now), mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
				InDoubtWindow: time.Minute, KeepContent: keep}, log),
			Keys: ks, Ping: db.Ping, Driver: capture.Name, MaxBodyBytes: 1 << 20, Log: log,
		})
	}
	call := func(h http.Handler, method, path, key, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	on := serving(time.Hour)
	marks := []string{"KEPTSUBJECTQ", "KEPTTEXTQ", "KEPTHTMLQ", "KEPTHEADERQ", "KEPTNAMEQ", "KEPTFILEQ"}
	code, sent := call(on, "POST", "/emails", sender, `{"from":"KEPTNAMEQ <app@example.com>","to":"kept@example.org",
		"subject":"KEPTSUBJECTQ","text":"KEPTTEXTQ","html":"<p>KEPTHTMLQ</p>","headers":{"X-Trace":"KEPTHEADERQ"},
		"attachments":[{"filename":"KEPTFILEQ.txt","content":"S0VQVEJZVEVTUQ=="}]}`)
	var made struct{ ID string }
	if json.Unmarshal([]byte(sent), &made); code != 200 || made.ID == "" {
		t.Fatalf("the send: %d %s", code, sent)
	}
	_, shown := call(on, "GET", "/emails/"+made.ID, admin, "")
	_, own := call(on, "GET", "/emails/"+made.ID, sender, "")
	kept, _, err := db.Content(ctx, made.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Its bounce comes with the receiving server's sentence, which is kept
	// under the same rule, by a reader built to read it.
	if err := db.Sent(ctx, made.ID, "<kept-"+made.ID+">"); err != nil {
		t.Fatal(err)
	}
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ack") {
			io.WriteString(w, `{"success":true,"result":{}}`)
			return
		}
		event := `{"type":"cf.email.sending.message.bounced","payload":{"eventId":"kept-` + made.ID + `","messageId":"<kept-` + made.ID + `>",` +
			`"subject":"EVENTSUBJECTQ","bounce":{"type":"soft","reason":"KEPTDETAILQ"}}}`
		body, _ := json.Marshal(map[string]any{"success": true, "result": map[string]any{
			"messages": []map[string]any{{"body": event, "lease_id": "l1"}}}})
		w.Write(body)
	}))
	defer queue.Close()
	reader := events.New(db, db.Ping, cloudflare.NewAt(queue.URL, "token").Events("q").WithDetails(), capture.Name,
		events.Options{Poll: time.Second, Batch: 10, Lease: time.Minute, Retry: time.Second, Grace: time.Minute,
			KeepDetails: time.Hour}, time.Now, log)
	if n, err := reader.Pass(ctx); n != 1 || err != nil {
		t.Fatalf("the event: %d %v", n, err)
	}
	_, shown = call(on, "GET", "/emails/"+made.ID, admin, "")
	_, own = call(on, "GET", "/emails/"+made.ID, sender, "")
	if !strings.Contains(shown, `"detail":"KEPTDETAILQ"`) || strings.Contains(own, "KEPTDETAILQ") {
		t.Errorf("the server's sentence: an admin key sees %s, the sending key %s", shown, own)
	}
	marks = append(marks, "KEPTDETAILQ")
	elsewhere, everywhere := dump(t, dsn, "message_contents", "event_details"), dump(t, dsn)
	if strings.Contains(everywhere, "EVENTSUBJECTQ") || strings.Contains(logged.String(), "EVENTSUBJECTQ") {
		t.Error("the subject an event carried was kept or logged")
	}
	// What a file was is kept with the words; its bytes are kept nowhere.
	if strings.Contains(everywhere, "KEPTBYTESQ") || strings.Contains(everywhere, "S0VQVEJZVEVTUQ") {
		t.Error("a file's bytes were kept")
	}
	// The dump reads inside the two tables: without that, finding no mark
	// in them would prove nothing, here or where nothing is to be kept.
	if !strings.Contains(everywhere, "KEPTSUBJECTQ") || !strings.Contains(everywhere, "KEPTDETAILQ") {
		t.Fatal("the dump cannot read the kept words; its checks prove nothing")
	}
	if !strings.Contains(elsewhere, made.ID) {
		t.Fatal("the dump is empty of this test's send; the check proves nothing")
	}
	for _, mark := range marks {
		if !strings.Contains(shown, mark) {
			t.Errorf("%q was not kept, or not shown to an admin key", mark)
		}
		if mark != "KEPTDETAILQ" && !strings.Contains(string(kept), mark) {
			t.Errorf("%q is not in the kept content", mark)
		}
		if strings.Contains(own, mark) {
			t.Errorf("the key that sent it was shown %q", mark)
		}
		if strings.Contains(elsewhere, mark) {
			t.Errorf("another table holds %q", mark)
		}
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
	// With the setting off again, what was kept is hidden at once.
	if _, hidden := call(serving(0), "GET", "/emails/"+made.ID, admin, ""); strings.Contains(hidden, "KEPT") {
		t.Errorf("with the setting off: %s", hidden)
	}
}
