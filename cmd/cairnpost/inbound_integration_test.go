//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/postgres"
	"github.com/mettjs/cairnpost/internal/webhook"
	"github.com/mettjs/cairnpost/migrations"
)

// An email that comes is kept in its one table, as it came, for its
// time; an endpoint is told who and when and never what it says; no log
// line holds it; and when its time is up it is hidden at once and then
// deleted with what was still owed for it.
func TestAnEmailThatComesIsKeptInOnePlaceForItsTime(t *testing.T) {
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
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ks := keys.New(db)
	full, k, err := ks.Create(ctx, t.Name(), store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	domain := strings.ReplaceAll(strings.ToLower(k.Prefix), "_", "-") + ".example.com"
	const secret = "inbound-secret-of-the-integration-test-0123"
	var hooks []string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hooks = append(hooks, string(body))
	}))
	defer app.Close()
	poster := webhook.NewPoster(time.Second, true, 2, "test", time.Now)
	endpoints := webhook.NewService(db, poster, true, 20, 24*time.Hour, time.Now)
	endpoint, err := endpoints.Create(ctx, "", webhook.New{URL: app.URL, Domain: domain, Types: []string{webhook.TypeReceived}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endpoints.Delete(ctx, "", endpoint.ID) })
	came := inbound.New(db, inbound.Options{Secret: secret, Keep: time.Hour})
	h := api.New(api.Deps{Mail: mail.New(db, capture.New(5, time.Now), mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute}, log),
		Keys: ks, Inbound: came, Webhooks: endpoints, Ping: db.Ping, Driver: capture.Name, MaxBodyBytes: 1 << 20, Log: log})
	raw := "From: Ana <ana@example.org>\r\nSubject: CAMESUBJECTQ\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nCAMEBODYQ " + k.Prefix + "\r\n--b\r\nContent-Type: application/pdf; name=CAMEFILEQ.pdf\r\n\r\nCAMEBYTESQ\r\n--b--\r\n"
	to := "help@" + domain
	handOn := func() (int, string) {
		sum := sha256.Sum256([]byte(raw))
		e := inbound.Envelope{Timestamp: strconv.FormatInt(time.Now().Unix(), 10), To: to, From: "ana@example.org", Digest: hex.EncodeToString(sum[:])}
		req := httptest.NewRequest("POST", "/inbound", strings.NewReader(raw))
		for name, v := range map[string]string{inbound.HeaderTimestamp: e.Timestamp, inbound.HeaderTo: e.To, inbound.HeaderFrom: e.From,
			inbound.HeaderDigest: e.Digest, inbound.HeaderSignature: inbound.Sign(secret, e)} {
			req.Header.Set(name, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out struct{ ID string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.ID
	}
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+full)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	code, id := handOn()
	if code != 201 || id == "" {
		t.Fatalf("handed on: %d %s", code, logged.String())
	}
	if code, again := handOn(); code != 200 || again != id {
		t.Fatalf("handed on twice: %d %s", code, again)
	}
	if rec := get("/inbound/" + id); rec.Code != 200 || !strings.Contains(rec.Body.String(), "CAMESUBJECTQ") || !strings.Contains(rec.Body.String(), "CAMEBODYQ") {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := get("/inbound/" + id + "/attachments/1"); rec.Code != 200 || rec.Body.String() != "CAMEBYTESQ" {
		t.Fatalf("its file: %d %q", rec.Code, rec.Body)
	}
	if rec := get("/inbound/" + id + "/raw"); rec.Code != 200 || rec.Body.String() != raw {
		t.Fatalf("as it came: %d", rec.Code)
	}
	if rec := get("/inbound"); rec.Code != 200 || !strings.Contains(rec.Body.String(), id) || strings.Contains(rec.Body.String(), "CAMESUBJECTQ") {
		t.Fatalf("the list: %d %s", rec.Code, rec.Body)
	}
	// The endpoint is told once, with who and when and nothing it says.
	worker := webhook.NewWorker(db, poster, webhook.Options{Poll: 50 * time.Millisecond, Workers: 4, PerEndpoint: 2, Retries: []time.Duration{time.Minute}}, log)
	if _, err := worker.Pass(ctx); err != nil || len(hooks) != 1 {
		t.Fatalf("the webhook: %d received, %v", len(hooks), err)
	}
	if !strings.Contains(hooks[0], `"type":"email.received"`) || !strings.Contains(hooks[0], `"inbound_id":"`+id+`"`) ||
		!strings.Contains(hooks[0], `"to":["`+to+`"]`) || !strings.Contains(hooks[0], `"from":"ana@example.org"`) || strings.Contains(hooks[0], "CAME") {
		t.Fatalf("the webhook says %s", hooks[0])
	}
	list, _, err := endpoints.Deliveries(ctx, "", endpoint.ID, "", "", 10)
	if err != nil || len(list) != 1 || list[0].Kind != store.KindReceived || list[0].InboundID != id || list[0].State != store.DeliveryDelivered {
		t.Fatalf("%+v %v", list, err)
	}
	everywhere, elsewhere := dump(t, dsn), dump(t, dsn, "inbound_emails")
	if !strings.Contains(everywhere, "CAMEBODYQ "+k.Prefix) || !strings.Contains(elsewhere, id) {
		t.Fatal("the dump cannot read the email, or is empty of this test; the check proves nothing")
	}
	for _, mark := range []string{"CAMESUBJECTQ", "CAMEBODYQ", "CAMEFILEQ", "CAMEBYTESQ", secret} {
		if strings.Contains(elsewhere, mark) {
			t.Errorf("another table holds %q", mark)
		}
		if strings.Contains(logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
	if strings.Contains(logged.String(), to) || strings.Contains(logged.String(), "ana@example.org") {
		t.Error("the log holds an address")
	}
	// Its time up, it is hidden at once, and then deleted with what an
	// endpoint was sent for it.
	if _, err := conn.Exec(ctx, `UPDATE inbound_emails SET expires_at = now() - interval '1 second' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/inbound/" + id, "/inbound/" + id + "/raw", "/inbound/" + id + "/attachments/1"} {
		if rec := get(path); rec.Code != 404 {
			t.Errorf("%s past its time: %d", path, rec.Code)
		}
	}
	if rec := get("/inbound"); strings.Contains(rec.Body.String(), id) {
		t.Error("listed past its time")
	}
	if _, err := db.PruneInbound(ctx, 100000); err != nil {
		t.Fatal(err)
	}
	var emails, owed int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM inbound_emails WHERE id = $1::uuid),
		(SELECT count(*) FROM webhook_deliveries WHERE endpoint_id = $2::uuid)`, id, endpoint.ID).Scan(&emails, &owed); err != nil || emails+owed != 0 {
		t.Errorf("pruned: %d emails and %d deliveries are left: %v", emails, owed, err)
	}
	// Handed on again after that, it is a new email.
	if code, anew := handOn(); code != 201 || anew == id {
		t.Errorf("handed on once the first is gone: %d", code)
	}
}
