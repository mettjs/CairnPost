package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
	"github.com/mettjs/cairnpost/internal/suppress"
	"github.com/mettjs/cairnpost/internal/templates"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// fake is a driver that keeps what it is handed, or answers an error.
type fake struct {
	mu   sync.Mutex
	err  error
	held []driver.Message
	// refuse answers an error for an email to that address.
	refuse map[string]error
}

func (*fake) Name() string { return "fake" }

func (f *fake) Send(_ context.Context, m driver.Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if err := f.refuse[m.To[0]]; err != nil {
		return "", err
	}
	f.held = append(f.held, m)
	return "up-1", nil
}

// down is a store whose every send fails before it starts.
type down struct{ store.Store }

func (down) Start(context.Context, store.Start) (store.Claim, error) {
	return store.Claim{}, errors.New("the database is gone")
}

// inboundSecret is what the rig's emails are handed on under.
const inboundSecret = "inbound-secret-of-the-test-rig-0123456789"

type rig struct {
	t           *testing.T
	h           http.Handler
	drv         *fake
	keys        *keys.Service
	send, admin string
}

// otherSendKey is a second key that may only send.
func otherSendKey(t *testing.T, r *rig) string {
	t.Helper()
	full, _, err := r.keys.Create(context.Background(), "another app", store.ScopeSend, "")
	if err != nil {
		t.Fatal(err)
	}
	return full
}

// newRig builds the API over a store whose clock stands still, so a wait
// the API says is exact.
func newRig(t *testing.T) *rig {
	s := memstore.New()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.Now = func() time.Time { return at }
	return newRigOn(t, s)
}

// newRigWith is newRig with the API's dependencies adjusted.
func newRigWith(t *testing.T, adjust func(*api.Deps)) *rig {
	s := memstore.New()
	return newRigBuilt(t, s, adjust)
}

func newRigOn(t *testing.T, s store.Store) *rig { return newRigBuilt(t, s, func(*api.Deps) {}) }

func newRigBuilt(t *testing.T, s store.Store, adjust func(*api.Deps)) *rig {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	drv := &fake{}
	ks := keys.New(s)
	send, _, err := ks.Create(context.Background(), "app", store.ScopeSend, "")
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := ks.Create(context.Background(), "ops", store.ScopeAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	written := templates.NewService(s, templates.Options{Max: 3, MaxBytes: 4096})
	deps := api.Deps{
		Mail: mail.New(s, drv, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
			InDoubtWindow: time.Minute, Templates: written, MaxFilled: 600}, log),
		Templates: written,
		Inbound:   inbound.New(s, inbound.Options{Secret: inboundSecret, Room: 4000}),
		Keys:      ks, Suppress: suppress.New(s), Ping: s.Ping,
		Webhooks: webhook.NewService(s, webhook.NewPoster(time.Second, true, 2, "test", time.Now), true, 20, 24*time.Hour, time.Now), Driver: "fake", Version: "test", MaxBodyBytes: 4096, Log: log,
		Captured: func() []api.CapturedEmail {
			var out []api.CapturedEmail
			for _, m := range drv.held {
				out = append(out, api.CapturedEmail{From: m.From, To: m.To, Subject: m.Subject})
			}
			return out
		},
	}
	adjust(&deps)
	return &rig{t: t, h: api.New(deps), drv: drv, keys: ks, send: send, admin: admin}
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

// raw answers a response's body as it was written.
func (r *rig) raw(method, path, key string) string {
	r.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestSendAnswersAnIdAndReplaysIt(t *testing.T) {
	r := newRig(t)
	code, _, first := r.do("POST", "/emails", r.send, "k1", good)
	if code != 200 || first["id"] == "" {
		t.Fatalf("send: %d %v", code, first)
	}
	code, _, again := r.do("POST", "/emails", r.send, "k1", good)
	if code != 200 || again["id"] != first["id"] || len(r.drv.held) != 1 {
		t.Fatalf("replay: %d %v, held %d", code, again, len(r.drv.held))
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
		{why: "an unknown field", key: "send", body: `{"from":"a@example.com","to":"b@example.org","subject":"s","text":"t","tags":["x"]}`, code: 422, name: "validation_error"},
		{why: "an address named twice", key: "send", body: `{"from":"a@example.com","to":["b@example.org"],"cc":"B@Example.org","subject":"s","text":"t"}`, code: 422, name: "validation_error"},
		{why: "nobody to send to", key: "send", body: `{"from":"a@example.com","to":[],"bcc":"b@example.org","subject":"s","text":"t"}`, code: 422, name: "validation_error"},
		{why: "what is no address among several", key: "send", body: `{"from":"a@example.com","to":["b@example.org","nobody"],"subject":"s","text":"t"}`, code: 422, name: "validation_error"},
		{why: "too large", key: "send", body: `{"text":"` + strings.Repeat("x", 5000) + `"}`, code: 413, name: "request_too_large"},
		{why: "refused for good", key: "send", body: good, driverErr: driver.ErrRejected, code: 422, name: "rejected_by_upstream"},
		{why: "refused, from a domain whose DNS is not set up", key: "send", body: good,
			driverErr: &mail.SenderDomain{Domain: "example.com"}, code: 422, name: "sender_domain_not_ready"},
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
	req := httptest.NewRequest("POST", "/emails", strings.NewReader(good))
	req.Header.Set("Authorization", "Bearer "+r.send)
	req.Header.Set("Idempotency-Key", "k")
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	// The wait said is the in-doubt window's remainder, not a guess.
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "concurrent_idempotent_requests") ||
		rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("in flight: %d %s, Retry-After %q", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
	r.do("POST", "/emails", r.send, "k2", good)
	other := strings.Replace(good, "Body text", "Another body", 1)
	if code, name, _ := r.do("POST", "/emails", r.send, "k2", other); code != 409 || name != "invalid_idempotent_request" {
		t.Fatalf("mismatch: %d %s", code, name)
	}
}

// A failure of our own, before the provider was asked, never says that an
// email may have gone.
func TestOurOwnFailureSaysNothingWasSent(t *testing.T) {
	r := newRigOn(t, down{memstore.New()})
	code, name, body := r.do("POST", "/emails", r.send, "k", good)
	if code != 503 || name != "unavailable" || !strings.Contains(body["message"].(string), "nothing was sent") {
		t.Fatalf("%d %s %v", code, name, body)
	}
}

// A send that failed after it was recorded answers its id, so its caller
// can ask what became of it.
func TestAFailedSendAnswersItsId(t *testing.T) {
	r := newRig(t)
	r.drv.err = errors.New("reset")
	code, _, body := r.do("POST", "/emails", r.send, "", good)
	id, _ := body["id"].(string)
	if code != 502 || id == "" {
		t.Fatalf("%d %v", code, body)
	}
	if code, _, got := r.do("GET", "/emails/"+id, r.send, "", ""); code != 200 || got["state"] != "unknown" {
		t.Fatalf("its record: %d %v", code, got)
	}
}

func TestValidationSaysWhatToChange(t *testing.T) {
	r := newRig(t)
	_, _, body := r.do("POST", "/emails", r.send, "", `{"from":"a@example.com","to":"b@example.org","subject":"s"}`)
	if body["message"] != `Give "html", "text" or both: an email needs a body.` {
		t.Fatalf("%v", body["message"])
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
	if list, ok := got["events"].([]any); !ok || len(list) != 0 {
		t.Fatalf("an email with no events yet answered %v", got["events"])
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

// An admin key that manages one sending domain reads that domain's
// emails, as it manages that domain's everything else, and no other's.
func TestAKeyBoundToADomainReadsOnlyItsOwnEmails(t *testing.T) {
	var ours, theirs string
	r := newRigWith(t, func(d *api.Deps) {
		var err error
		if ours, _, err = d.Keys.Create(context.Background(), "tenant", store.ScopeAdmin, "example.com"); err != nil {
			t.Fatal(err)
		}
		if theirs, _, err = d.Keys.Create(context.Background(), "neighbour", store.ScopeAdmin, "other.example"); err != nil {
			t.Fatal(err)
		}
	})
	_, _, sent := r.do("POST", "/emails", r.send, "", good)
	path := "/emails/" + sent["id"].(string)
	if code, _, got := r.do("GET", path, ours, "", ""); code != 200 || got["to"] != "ana@example.org" {
		t.Fatalf("its own domain's email: %d", code)
	}
	if code, name, got := r.do("GET", path, theirs, "", ""); code != 404 || name != "not_found" || got["to"] != nil {
		t.Fatalf("another domain's email: %d %s", code, name)
	}
	// An email it sent itself is its own to read.
	_, _, own := r.do("POST", "/emails", theirs, "", `{"from":"app@other.example","to":"bo@example.org","subject":"Hello","text":"Body"}`)
	if code, _, _ := r.do("GET", "/emails/"+own["id"].(string), theirs, "", ""); code != 200 {
		t.Fatalf("the email it sent: %d", code)
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

func TestSuppressionsAreAnAdminsToManage(t *testing.T) {
	r := newRig(t)
	const body = `{"domain":"example.com","address":"ana@example.org","note":"asked us to stop"}`
	const one = `{"domain":"example.com","address":"ana@example.org"}`
	for _, call := range [][2]string{{"GET", "/suppressions"}, {"POST", "/suppressions"}, {"POST", "/suppressions/lift"}} {
		if code, name, _ := r.do(call[0], call[1], r.send, "", body); code != 403 || name != "admin_key_required" {
			t.Fatalf("%s %s with a send key: %d %s", call[0], call[1], code, name)
		}
	}
	if code, _, got := r.do("POST", "/suppressions", r.admin, "", body); code != 201 || got["added"] != true {
		t.Fatalf("add: %d %v", code, got)
	}
	if code, _, got := r.do("POST", "/suppressions", r.admin, "", body); code != 200 || got["added"] != false {
		t.Fatalf("add again: %d %v", code, got)
	}
	// The address is now refused, as the request's own fault, and says how
	// to undo it.
	code, name, got := r.do("POST", "/emails", r.send, "", good)
	if code != 422 || name != "recipient_suppressed" || !strings.Contains(got["message"].(string), "POST /suppressions/lift") || got["id"] != nil {
		t.Fatalf("a send to it: %d %s %v", code, name, got)
	}
	code, _, got = r.do("GET", "/suppressions?domain=example.com", r.admin, "", "")
	list, _ := got["data"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["reason"] != "manual" || got["next"] != nil {
		t.Fatalf("list: %d %v", code, got)
	}
	for what, c := range map[string]struct {
		method, path, body string
		code               int
		name               string
	}{
		"a bad address":       {"POST", "/suppressions", `{"domain":"example.com","address":"nobody"}`, 422, "validation_error"},
		"a bad limit":         {"GET", "/suppressions?limit=0", "", 422, "validation_error"},
		"a page too large":    {"GET", "/suppressions?limit=201", "", 422, "validation_error"},
		"a made-up cursor":    {"GET", "/suppressions?after=AGEAYg", "", 422, "validation_error"},
		"a domain that hides": {"GET", "/suppressions?domain=a%00b", "", 422, "validation_error"},
		"a body too large":    {"POST", "/suppressions", `{"domain":"example.com","note":"` + strings.Repeat("n", 5000) + `"}`, 413, "request_too_large"},
		"lift of nothing":     {"POST", "/suppressions/lift", `{"domain":"example.com"}`, 422, "validation_error"},
		"lift of both":        {"POST", "/suppressions/lift", `{"domain":"example.com","address":"a@example.org","all":true}`, 422, "validation_error"},
		"a reason for one":    {"POST", "/suppressions/lift", `{"domain":"example.com","address":"a@example.org","reason":"bounce"}`, 422, "validation_error"},
		"the old way to lift": {"DELETE", "/suppressions?domain=example.com&address=ana@example.org", "", 405, "method_not_allowed"},
	} {
		if code, name, _ := r.do(c.method, c.path, r.admin, "", c.body); code != c.code || name != c.name {
			t.Errorf("%s: %d %s", what, code, name)
		}
	}
	if code, _, got := r.do("POST", "/suppressions/lift", r.admin, "", one); code != 200 || got["lifted"] != float64(1) {
		t.Fatalf("lift: %d %v", code, got)
	}
	if code, name, _ := r.do("POST", "/suppressions/lift", r.admin, "", one); code != 404 || name != "not_found" {
		t.Fatalf("lift again: %d %s", code, name)
	}
	if code, _, _ := r.do("POST", "/emails", r.send, "", good); code != 200 {
		t.Fatalf("a send once lifted: %d", code)
	}
	for _, address := range []string{"a@example.org", "b@example.org"} {
		r.do("POST", "/suppressions", r.admin, "", `{"domain":"example.com","address":"`+address+`"}`)
	}
	if code, _, got := r.do("POST", "/suppressions/lift", r.admin, "", `{"domain":"example.com","all":true,"reason":"manual"}`); code != 200 || got["lifted"] != float64(2) {
		t.Fatalf("lift all: %d %v", code, got)
	}
}

// An admin key bound to a sending domain manages that domain's list and no
// other's.
func TestAKeyBoundToADomainManagesOnlyItsOwnSuppressions(t *testing.T) {
	var bound string
	r := newRigWith(t, func(d *api.Deps) {
		var err error
		if bound, _, err = d.Keys.Create(context.Background(), "tenant", store.ScopeAdmin, "example.com"); err != nil {
			t.Fatal(err)
		}
	})
	r.do("POST", "/suppressions", r.admin, "", `{"domain":"other.example","address":"ana@example.org"}`)
	r.do("POST", "/suppressions", r.admin, "", `{"domain":"example.com","address":"bo@example.org"}`)
	for what, c := range map[string][3]string{
		"list":     {"GET", "/suppressions?domain=other.example", ""},
		"add":      {"POST", "/suppressions", `{"domain":"other.example","address":"cy@example.org"}`},
		"lift":     {"POST", "/suppressions/lift", `{"domain":"other.example","address":"ana@example.org"}`},
		"lift all": {"POST", "/suppressions/lift", `{"domain":"other.example","all":true}`},
	} {
		if code, name, _ := r.do(c[0], c[1], bound, "", c[2]); code != 403 || name != "domain_not_allowed" {
			t.Errorf("%s of another domain's: %d %s", what, code, name)
		}
	}
	// Asked for all of them, it is shown its own.
	code, _, got := r.do("GET", "/suppressions", bound, "", "")
	list, _ := got["data"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["domain"] != "example.com" {
		t.Fatalf("its own list: %d %v", code, got)
	}
	if code, _, _ := r.do("POST", "/suppressions/lift", bound, "", `{"domain":"Example.com","address":"bo@example.org"}`); code != 200 {
		t.Fatalf("lifting its own: %d", code)
	}
}

func TestLimitsSayWhatTheProviderAllows(t *testing.T) {
	r := newRig(t)
	if code, _, got := r.do("GET", "/limits", r.send, "", ""); code != 200 || got["quota"] != nil || got["driver"] != "fake" {
		t.Fatalf("a provider with no allowance to tell: %d %v", code, got)
	}
	if code, _, _ := r.do("GET", "/limits", "", "", ""); code != 401 {
		t.Fatalf("without a key: %d", code)
	}
	resets := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	r = newRigWith(t, func(d *api.Deps) {
		d.Quota = func(context.Context) (api.Quota, error) {
			return api.Quota{Limit: 200, Per: "day", Used: 7, ResetsAt: &resets}, nil
		}
	})
	code, _, got := r.do("GET", "/limits", r.send, "", "")
	quota, _ := got["quota"].(map[string]any)
	if code != 200 || quota["limit"] != 200.0 || quota["used"] != 7.0 || quota["per"] != "day" || quota["over"] != false {
		t.Fatalf("%d %v", code, got)
	}
	r = newRigWith(t, func(d *api.Deps) {
		d.Quota = func(context.Context) (api.Quota, error) { return api.Quota{}, errors.New("down") }
	})
	if code, name, _ := r.do("GET", "/limits", r.send, "", ""); code != 502 || name != "upstream_unknown" {
		t.Fatalf("a provider that does not answer: %d %s", code, name)
	}
	r = newRigWith(t, func(d *api.Deps) {
		d.Quota = func(context.Context) (api.Quota, error) { return api.Quota{}, driver.ErrUnavailable }
	})
	if code, name, _ := r.do("GET", "/limits", r.send, "", ""); code != 503 || name != "upstream_unavailable" {
		t.Fatalf("a provider that refuses: %d %s", code, name)
	}
	r = newRigWith(t, func(d *api.Deps) {
		d.Quota = func(context.Context) (api.Quota, error) {
			return api.Quota{}, &driver.Throttled{After: 30 * time.Second}
		}
	})
	if code, name, _ := r.do("GET", "/limits", r.send, "", ""); code != 429 || name != "rate_limit_exceeded" {
		t.Fatalf("a provider asked too often: %d %s", code, name)
	}
}

func TestHealthNeedsNoKey(t *testing.T) {
	r := newRig(t)
	if code, _, got := r.do("GET", "/health", "", "", ""); code != 200 || got["status"] != "ok" || got["driver"] != "fake" {
		t.Fatalf("%d %v", code, got)
	}
	if _, _, got := r.do("GET", "/health", "", "", ""); got["events"] != "off" || got["webhooks"] != "off" || got["pruning"] != "off" {
		t.Fatalf("with no events to read and no webhooks sent: %v", got)
	}
	for healthy, want := range map[bool][3]string{true: {"reading", "sending", "running"}, false: {"failing", "failing", "failing"}} {
		r := newRigWith(t, func(d *api.Deps) {
			d.Events, d.Sending, d.Pruning = func() bool { return healthy }, func() bool { return healthy }, func() bool { return healthy }
		})
		// Events not read, or webhooks not sent, do not make the service
		// unavailable: mail still goes.
		if code, _, got := r.do("GET", "/health", "", "", ""); code != 200 || got["events"] != want[0] || got["webhooks"] != want[1] || got["pruning"] != want[2] {
			t.Fatalf("healthy=%v: %d %v", healthy, code, got)
		}
	}
	// A path that is not there is named apart from a record that is not,
	// so a wrong server address is not read as a missing email; a path
	// that is there under another method says which.
	for path, want := range map[string]string{"/nothing": "route_not_found", "/api/emails/abc": "route_not_found",
		"/emails/": "route_not_found"} {
		if code, name, _ := r.do("GET", path, "", "", ""); code != 404 || name != want {
			t.Errorf("GET %s: %d %s", path, code, name)
		}
	}
	req := httptest.NewRequest("PUT", "/emails/abc", nil)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD, PATCH" || !strings.Contains(rec.Body.String(), `"method_not_allowed"`) ||
		!strings.Contains(rec.Body.String(), "It takes: GET, HEAD, PATCH.") {
		t.Fatalf("a wrong method: %d Allow %q %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
}

// An email goes to several addresses under one id; what became of each
// is answered with it, and a suppressed one is named by the send.
func TestAnEmailToSeveralIsOneIdWithEachAddress(t *testing.T) {
	r := newRig(t)
	if code, _, _ := r.do("POST", "/suppressions", r.admin, "", `{"domain":"example.com","address":"gone@example.org"}`); code != 200 && code != 201 {
		t.Fatalf("suppressing: %d", code)
	}
	code, _, sent := r.do("POST", "/emails", r.send, "", `{"from":"app@example.com","to":["Ana <ana@example.org>","gone@example.org"],
		"cc":"bo@example.org","bcc":["cy@example.org"],"subject":"s","text":"t"}`)
	id, _ := sent["id"].(string)
	left, _ := sent["suppressed"].([]any)
	if code != 200 || id == "" || len(left) != 1 || left[0] != "gone@example.org" {
		t.Fatalf("the send: %d %v", code, sent)
	}
	if m := r.drv.held[len(r.drv.held)-1]; len(m.To) != 1 || m.To[0] != "ana@example.org" || len(m.Cc) != 1 || len(m.Bcc) != 1 {
		t.Fatalf("the driver was handed %+v", m)
	}
	code, _, got := r.do("GET", "/emails/"+id, r.send, "", "")
	all, _ := got["recipients"].([]any)
	out, _ := got["suppressed"].([]any)
	if code != 200 || got["to"] != "ana@example.org" || len(all) != 3 || len(out) != 1 || out[0] != "gone@example.org" {
		t.Fatalf("the email: %d %v", code, got)
	}
	for i, want := range [][2]string{{"ana@example.org", "to"}, {"bo@example.org", "cc"}, {"cy@example.org", "bcc"}} {
		one, _ := all[i].(map[string]any)
		if one["address"] != want[0] || one["kind"] != want[1] || one["state"] != "sent" {
			t.Errorf("address %d: %v", i, one)
		}
	}
	// A send to everyone answers no list of those left out.
	if _, _, whole := r.do("POST", "/emails", r.send, "", good); whole["suppressed"] != nil {
		t.Errorf("a send to all its addresses says some were left out: %v", whole)
	}
	// A list a client left empty, written as null or "", is no address.
	if code, _, got := r.do("POST", "/emails", r.send, "", `{"from":"app@example.com","to":"ana@example.org","cc":null,"bcc":"","subject":"s","text":"t"}`); code != 200 {
		t.Errorf("with empty lists: %d %v", code, got)
	}
	if code, name, _ := r.do("POST", "/emails", r.send, "", `{"from":"app@example.com","to":null,"subject":"s","text":"t"}`); code != 422 || name != "validation_error" {
		t.Errorf("with no to: %d %s", code, name)
	}
}

// A send carries its files in base64 and may be larger for them than
// any other request; what is not base64 is refused by the file's place.
func TestASendCarriesItsFiles(t *testing.T) {
	r := newRigWith(t, func(d *api.Deps) { d.MaxEmailBytes = 64 << 10 })
	large := base64.StdEncoding.EncodeToString(make([]byte, 20<<10))
	code, _, sent := r.do("POST", "/emails", r.send, "", `{"from":"app@example.com","to":"ana@example.org","subject":"s","html":"<img src=cid:logo>",
		"attachments":[{"filename":"big.bin","content":"`+large+`"},
			{"filename":"logo.png","content":"AAEC","content_type":"image/png","content_id":"logo"}]}`)
	if code != 200 || sent["id"] == "" {
		t.Fatalf("the send: %d %v", code, sent)
	}
	got := r.drv.held[len(r.drv.held)-1].Attachments
	if len(got) != 2 || len(got[0].Content) != 20<<10 || got[0].Type != "application/octet-stream" ||
		got[1].ContentID != "logo" || got[1].Type != "image/png" || !bytes.Equal(got[1].Content, []byte{0, 1, 2}) {
		t.Fatalf("the driver was handed %d files", len(got))
	}
	code, name, said := r.do("POST", "/emails", r.send, "", `{"from":"app@example.com","to":"ana@example.org","subject":"s","text":"t",
		"attachments":[{"filename":"a.txt","content":"AAEC"},{"filename":"SECRETNAME.txt","content":"not base64!"}]}`)
	if msg, _ := said["message"].(string); code != 422 || name != "validation_error" || !strings.Contains(msg, "Attachment 2") || strings.Contains(msg, "SECRETNAME") {
		t.Fatalf("what is not base64: %d %s %v", code, name, said)
	}
	// Any other request is still held to the smaller size.
	if code, name, _ := r.do("POST", "/suppressions", r.admin, "", `{"domain":"example.com","address":"a@example.org","note":"`+strings.Repeat("n", 8<<10)+`"}`); code != 413 || name != "request_too_large" {
		t.Fatalf("a large body elsewhere: %d %s", code, name)
	}
	if code, name, _ := r.do("POST", "/emails", r.send, "", `{"from":"app@example.com","to":"ana@example.org","subject":"s","text":"t",
		"attachments":[{"filename":"a.bin","content":"`+base64.StdEncoding.EncodeToString(make([]byte, 64<<10))+`"}]}`); code != 413 || name != "request_too_large" {
		t.Fatalf("a send over its size: %d %s", code, name)
	}
}
