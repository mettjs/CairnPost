package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

func email(to string) string {
	return fmt.Sprintf(`{"from":"App <app@example.com>","to":%q,"subject":"Hello","text":"Body text"}`, to)
}

func items(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	list, _ := body["data"].([]any)
	out := make([]map[string]any, len(list))
	for i, one := range list {
		out[i], _ = one.(map[string]any)
	}
	return out
}

func TestABatchAnswersEachEmailInItsPlace(t *testing.T) {
	r := newRig(t)
	code, _, body := r.do("POST", "/emails/batch", r.send, "", "["+email("ana@example.org")+","+email("ben@example.org")+"]")
	got := items(t, body)
	if code != 200 || len(got) != 2 || got[0]["id"] == "" || got[0]["id"] == got[1]["id"] || got[1]["error"] != nil {
		t.Fatalf("a batch of two: %d %v", code, body)
	}
	for i, to := range []string{"ana@example.org", "ben@example.org"} {
		_, _, read := r.do("GET", "/emails/"+got[i]["id"].(string), r.send, "", "")
		if read["to"] != to || read["state"] != "sent" {
			t.Errorf("email %d reads %v", i+1, read)
		}
	}
}

func TestABatchWithAnEmailThatCanNeverGoSendsNothing(t *testing.T) {
	r := newRig(t)
	for name, body := range map[string]string{
		"validation_error":   "[" + email("ana@example.org") + "," + email("nobody") + "]",
		"validation_error 2": `[` + email("ana@example.org") + `,{"from":"app@example.com","to":"b@example.org","subject":"s","text":"t","attachments":[{"filename":"a.txt","content":"***"}]}]`,
		"validation_error 3": "[]",
		"validation_error 4": email("ana@example.org"),
	} {
		code, got, answer := r.do("POST", "/emails/batch", r.send, "", body)
		if code != 422 || got != strings.Fields(name)[0] || len(r.drv.held) != 0 {
			t.Errorf("%s: %d %s %v, %d sent", name, code, got, answer, len(r.drv.held))
		}
		if strings.HasSuffix(name, "error") && !strings.HasPrefix(answer["message"].(string), "Email 2 of the batch: ") {
			t.Errorf("the refusal does not name the email by its place: %v", answer["message"])
		}
	}
	// A key may not send what it could not send alone.
	if code, _, _ := r.do("POST", "/emails/batch", "", "", "[]"); code != 401 {
		t.Errorf("a batch with no key: %d", code)
	}
}

func TestABatchSaysWhichEmailsDidNotGo(t *testing.T) {
	r := newRig(t)
	r.drv.refuse = map[string]error{"ben@example.org": driver.ErrRejected,
		"cy@example.org": &driver.Throttled{After: 90 * time.Second}}
	req := httptest.NewRequest("POST", "/emails/batch",
		strings.NewReader("["+email("ana@example.org")+","+email("ben@example.org")+"]"))
	req.Header.Set("Authorization", "Bearer "+r.send)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if rec.Code != 207 || !strings.Contains(rec.Body.String(), `"name":"rejected_by_upstream"`) || len(r.drv.held) != 1 {
		t.Fatalf("a batch one of which is refused: %d %s", rec.Code, rec.Body)
	}
	code, _, body := r.do("POST", "/emails/batch", r.send, "", "["+email("cy@example.org")+"]")
	got := items(t, body)
	if code != 207 || got[0]["error"].(map[string]any)["name"] != "rate_limit_exceeded" {
		t.Fatalf("a throttled batch: %d %v", code, body)
	}
}

func TestABatchSentAgainSendsOnlyWhatDidNotGo(t *testing.T) {
	r := newRig(t)
	r.drv.refuse = map[string]error{"ben@example.org": driver.ErrUnavailable}
	batch := "[" + email("ana@example.org") + "," + email("ben@example.org") + "," + email("cy@example.org") + "]"
	code, _, body := r.do("POST", "/emails/batch", r.send, "order-7", batch)
	first := items(t, body)
	if code != 207 || len(r.drv.held) != 2 || first[1]["error"].(map[string]any)["name"] != "upstream_unavailable" {
		t.Fatalf("the first try: %d %v", code, body)
	}
	r.drv.refuse = nil
	code, _, body = r.do("POST", "/emails/batch", r.send, "order-7", batch)
	again := items(t, body)
	if code != 200 || len(r.drv.held) != 3 || again[0]["id"] != first[0]["id"] || again[2]["id"] != first[2]["id"] || again[1]["id"] == nil {
		t.Fatalf("the second try: %d %v, %d sent in all", code, body, len(r.drv.held))
	}
	// The same key with other emails is another request.
	code, _, body = r.do("POST", "/emails/batch", r.send, "order-7", "["+email("dee@example.org")+"]")
	if got := items(t, body); code != 207 || got[0]["error"].(map[string]any)["name"] != "invalid_idempotent_request" {
		t.Errorf("the key with another batch: %d %v", code, body)
	}
}

// A list is read one email after the other and no further than a batch
// may hold, whatever its size in bytes.
func TestABatchOfTooManyIsRefusedBeforeItIsHeld(t *testing.T) {
	r := newRigWith(t, func(d *api.Deps) { d.MaxBodyBytes = 1 << 20 })
	many := "[" + strings.Repeat("{},", 200000) + "{}]"
	before := testing.AllocsPerRun(1, func() {
		if code, name, _ := r.do("POST", "/emails/batch", r.send, "", many); code != 422 || name != "validation_error" {
			t.Errorf("a batch of 200001: %d %s", code, name)
		}
	})
	if before > 20000 {
		t.Errorf("refusing it took %v allocations: the whole list was read", before)
	}
	for _, bad := range []string{"[" + email("a@example.org") + "] []", "[" + email("a@example.org") + ",", `"emails"`} {
		if code, name, _ := r.do("POST", "/emails/batch", r.send, "", bad); code != 422 || name != "validation_error" || len(r.drv.held) != 0 {
			t.Errorf("%q: %d %s, %d sent", bad, code, name, len(r.drv.held))
		}
	}
	if code, name, _ := r.do("POST", "/emails/batch", r.send, strings.Repeat("k", 249), "["+email("a@example.org")+"]"); code != 422 || name != "validation_error" {
		t.Errorf("a key too long to number: %d %s", code, name)
	}
}

// An email of a batch is told when to try again by its own error, not by
// another email's.
func TestEachEmailSaysItsOwnWait(t *testing.T) {
	r := newRigWith(t, func(d *api.Deps) {})
	r.drv.refuse = map[string]error{"ben@example.org": driver.ErrUnavailable,
		"cy@example.org": &driver.Throttled{After: 90 * time.Second}}
	req := httptest.NewRequest("POST", "/emails/batch", strings.NewReader("["+email("ben@example.org")+","+email("cy@example.org")+"]"))
	req.Header.Set("Authorization", "Bearer "+r.send)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	var out struct {
		Data []struct {
			Error struct {
				Name       string `json:"name"`
				RetryAfter int    `json:"retry_after"`
			} `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 207 || rec.Header().Get("Retry-After") != "90" ||
		out.Data[0].Error.Name != "upstream_unavailable" || out.Data[0].Error.RetryAfter != 0 || out.Data[1].Error.RetryAfter != 90 {
		t.Fatalf("%d %s %s %v", rec.Code, rec.Header().Get("Retry-After"), rec.Body, err)
	}
}

// A key used for an email alone is not a place of a batch.
func TestABatchsKeyIsNotAnEmailsOwn(t *testing.T) {
	r := newRig(t)
	_, _, alone := r.do("POST", "/emails", r.send, "x#1", email("ana@example.org"))
	code, _, body := r.do("POST", "/emails/batch", r.send, "x", "["+email("ben@example.org")+"]")
	if got := items(t, body); code != 200 || got[0]["id"] == alone["id"] || len(r.drv.held) != 2 {
		t.Fatalf("%d %v after %v", code, body, alone)
	}
}

// A batch may take longer to answer than the server gives any other
// request, and is answered all the same.
func TestABatchOutlastsTheServersOwnTimeToAnswer(t *testing.T) {
	r := newRigWith(t, func(d *api.Deps) { d.BatchTime = 10 * time.Second })
	slow := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(400 * time.Millisecond)
		r.h.ServeHTTP(w, req)
	})
	srv := httptest.NewUnstartedServer(slow)
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	defer srv.Close()
	for path, want := range map[string]bool{"/emails/batch": true, "/emails": false} {
		body := email("ana@example.org")
		if want {
			body = "[" + body + "]"
		}
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+r.send)
		res, err := http.DefaultClient.Do(req)
		if answered := err == nil && res.StatusCode == 200; answered != want {
			t.Errorf("%s answered: %v (%v)", path, answered, err)
		}
		if res != nil {
			res.Body.Close()
		}
	}
}

func TestAnEmailWhoseTurnDidNotComeAnswers503(t *testing.T) {
	s := memstore.New()
	gone := make(chan struct{})
	close(gone)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := newRigBuilt(t, s, func(d *api.Deps) {
		d.Mail = mail.New(s, &fake{}, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute, Stopping: gone}, log)
	})
	code, _, body := r.do("POST", "/emails/batch", r.send, "", "["+email("ana@example.org")+"]")
	if got := items(t, body)[0]["error"].(map[string]any); code != 207 || got["name"] != "not_started" || got["statusCode"] != float64(503) {
		t.Fatalf("%d %v", code, body)
	}
}
