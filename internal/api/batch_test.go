package api_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
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
