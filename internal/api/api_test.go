package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// failing is a driver that answers an error; nil lets the capture driver
// underneath take the email.
type failing struct {
	*capture.Driver
	err error
}

func (f *failing) Send(ctx context.Context, m driver.Message) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.Driver.Send(ctx, m)
}

type rig struct {
	t           *testing.T
	h           http.Handler
	drv         *failing
	send, admin string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s, log := memstore.New(), slog.New(slog.NewTextHandler(io.Discard, nil))
	drv := &failing{Driver: capture.New(10, time.Now)}
	ks := keys.New(s)
	send, _, _ := ks.Create(context.Background(), "app", store.ScopeSend, "")
	admin, _, _ := ks.Create(context.Background(), "ops", store.ScopeAdmin, "")
	h := api.New(api.Deps{
		Mail: mail.New(s, drv, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
			InDoubtWindow: time.Minute}, log),
		Keys: ks, Ping: s.Ping, Capture: drv.Driver, Driver: "capture", Version: "test",
		MaxBodyBytes: 4096, Log: log,
	})
	return &rig{t: t, h: h, drv: drv, send: send, admin: admin}
}

const good = `{"from":"App <app@example.com>","to":"ana@example.org","subject":"Hello","text":"Body text"}`

// do answers the status, the error's name and the decoded body.
func (r *rig) do(method, path, key, idem, body string) (int, string, map[string]any) {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	name, _ := out["name"].(string)
	return rec.Code, name, out
}

func TestSendAnswersAnIdAndReplaysIt(t *testing.T) {
	r := newRig(t)
	code, _, first := r.do("POST", "/emails", r.send, "k1", good)
	if code != 200 || first["id"] == "" {
		t.Fatalf("send: %d %v", code, first)
	}
	code, _, again := r.do("POST", "/emails", r.send, "k1", good)
	if code != 200 || again["id"] != first["id"] || len(r.drv.Held()) != 1 {
		t.Fatalf("replay: %d %v, held %d", code, again, len(r.drv.Held()))
	}
}

// The statuses are a contract: a caller drops a message for good on 422,
// reads 409 invalid_idempotent_request as "already sent", and tries
// anything else again.
func TestEveryRefusalHasItsStatus(t *testing.T) {
	cases := []struct {
		why        string
		key, idem  string
		body       string
		driverErr  error
		code       int
		name       string
		retryAfter string
	}{
		{why: "no key", body: good, code: 401, name: "missing_api_key"},
		{why: "a wrong key", key: "cp_deadbeef_" + strings.Repeat("A", 43), body: good, code: 401, name: "invalid_api_key"},
		{why: "not JSON", key: "send", body: "{", code: 422, name: "validation_error"},
		{why: "an unknown field", key: "send", body: `{"from":"a@example.com","to":"b@example.org","subject":"s","text":"t","cc":"c@example.org"}`, code: 422, name: "validation_error"},
		{why: "two recipients", key: "send", body: `{"from":"a@example.com","to":["b@example.org","c@example.org"],"subject":"s","text":"t"}`, code: 422, name: "validation_error"},
		{why: "too large", key: "send", body: `{"text":"` + strings.Repeat("x", 5000) + `"}`, code: 413, name: "request_too_large"},
		{why: "refused for good", key: "send", body: good, driverErr: driver.ErrRejected, code: 422, name: "rejected_by_upstream"},
		{why: "at the limit", key: "send", body: good, driverErr: &driver.Throttled{After: 90 * time.Second}, code: 429, name: "rate_limit_exceeded", retryAfter: "90"},
		{why: "upstream down", key: "send", body: good, driverErr: driver.ErrUnavailable, code: 503, name: "upstream_unavailable"},
		{why: "answer lost", key: "send", idem: "lost", body: good, driverErr: errors.New("reset"), code: 502, name: "upstream_unknown"},
	}
	for _, c := range cases {
		r := newRig(t)
		r.drv.err = c.driverErr
		key := c.key
		if key == "send" {
			key = r.send
		}
		req := httptest.NewRequest("POST", "/emails", strings.NewReader(c.body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Idempotency-Key", c.idem)
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		var p struct{ Name, Message string }
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != c.code || p.Name != c.name || p.Message == "" || rec.Header().Get("Retry-After") != c.retryAfter {
			t.Errorf("%s: got %d %s (Retry-After %q), want %d %s", c.why, rec.Code, p.Name,
				rec.Header().Get("Retry-After"), c.code, c.name)
		}
	}
}

func TestIdempotencyConflictsAreTwoDifferent409s(t *testing.T) {
	r := newRig(t)
	r.drv.err = errors.New("reset")
	r.do("POST", "/emails", r.send, "k", good)
	r.drv.err = nil
	if code, name, _ := r.do("POST", "/emails", r.send, "k", good); code != 409 || name != "concurrent_idempotent_requests" {
		t.Fatalf("in flight: %d %s", code, name)
	}
	r.do("POST", "/emails", r.send, "k2", good)
	other := strings.Replace(good, "Body text", "Another body", 1)
	if code, name, _ := r.do("POST", "/emails", r.send, "k2", other); code != 409 || name != "invalid_idempotent_request" {
		t.Fatalf("mismatch: %d %s", code, name)
	}
}

func TestAnEmailIsReadByItsSenderAndByAdmins(t *testing.T) {
	r := newRig(t)
	_, _, sent := r.do("POST", "/emails", r.send, "", good)
	path := "/emails/" + sent["id"].(string)
	code, _, got := r.do("GET", path, r.send, "", "")
	if code != 200 || got["state"] != "sent" || got["to"] != "ana@example.org" {
		t.Fatalf("its sender: %d %v", code, got)
	}
	if _, has := got["subject"]; has {
		t.Fatal("an email's record answered a subject")
	}
	if code, _, _ := r.do("GET", path, r.admin, "", ""); code != 200 {
		t.Fatalf("an admin: %d", code)
	}
	other, _, _ := keys.New(memstore.New()).Create(context.Background(), "x", store.ScopeSend, "")
	if code, _, _ := r.do("GET", path, other, "", ""); code != 401 {
		t.Fatalf("a key of another server: %d", code)
	}
	if code, _, _ := r.do("GET", "/emails/nope", r.send, "", ""); code != 404 {
		t.Fatalf("an unknown id: %d", code)
	}
}

func TestCapturedMailNeedsAnAdminKey(t *testing.T) {
	r := newRig(t)
	r.do("POST", "/emails", r.send, "", good)
	if code, name, _ := r.do("GET", "/capture/emails", r.send, "", ""); code != 403 || name != "admin_key_required" {
		t.Fatalf("a send key: %d %s", code, name)
	}
	code, _, got := r.do("GET", "/capture/emails", r.admin, "", "")
	list, _ := got["data"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["subject"] != "Hello" {
		t.Fatalf("an admin key: %d %v", code, got)
	}
}

func TestHealthNeedsNoKey(t *testing.T) {
	r := newRig(t)
	if code, _, got := r.do("GET", "/health", "", "", ""); code != 200 || got["status"] != "ok" || got["driver"] != "capture" {
		t.Fatalf("%d %v", code, got)
	}
	if code, name, _ := r.do("GET", "/nothing", "", "", ""); code != 404 || name != "not_found" {
		t.Fatalf("an unknown path: %d %s", code, name)
	}
}
