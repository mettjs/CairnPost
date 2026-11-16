package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// stand is a stand-in for Cloudflare: it answers what it is told to and
// keeps the last request.
type stand struct {
	status int
	header map[string]string
	body   string
	hang   bool
	// limits is what the allowance endpoint answers; "" for a 404.
	limits string
	sends  int

	calls   int
	path    string
	auth    string
	request map[string]any
}

func (s *stand) serve(t *testing.T) *Driver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/limits") && s.limits != "" {
			io.WriteString(w, s.limits)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/send") {
			s.sends++
		}
		s.calls++
		s.path, s.auth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		s.request = nil
		_ = json.Unmarshal(raw, &s.request)
		if s.hang {
			<-r.Context().Done()
			return
		}
		for k, v := range s.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(s.status)
		io.WriteString(w, s.body)
	}))
	t.Cleanup(srv.Close)
	d := New("acct-1", "token-1")
	d.base = srv.URL + "/accounts/acct-1"
	return d
}

var message = driver.Message{From: "app@example.com", FromName: "José", To: []string{"ana@example.org"},
	ReplyTo: "help@example.com", Subject: "Hello", Text: "Body", Headers: map[string]string{"X-Trace": "t1"}}

const taken = `{"success":true,"errors":[],"result":{"message_id":"<abc@example.com>","delivered":[],
	"queued":["ana@example.org"],"permanent_bounces":[],"suppressed_recipients":[]}}`

func TestSendAsksCloudflareAsItWantsToBeAsked(t *testing.T) {
	s := &stand{status: 200, body: taken}
	id, err := s.serve(t).Send(context.Background(), message)
	if err != nil || id != "<abc@example.com>" {
		t.Fatalf("id %q, err %v", id, err)
	}
	from, _ := s.request["from"].(map[string]any)
	to, _ := s.request["to"].([]any)
	if s.path != "/accounts/acct-1/email/sending/send" || s.auth != "Bearer token-1" ||
		from["address"] != "app@example.com" || from["name"] != "José" || len(to) != 1 || to[0] != "ana@example.org" ||
		s.request["reply_to"] != "help@example.com" || s.request["subject"] != "Hello" || s.request["text"] != "Body" {
		t.Fatalf("the request: %s %s %v", s.path, s.auth, s.request)
	}
	if _, has := s.request["html"]; has {
		t.Fatal("an empty html was sent as a field")
	}
}

// Each answer of Cloudflare's is one of three kinds to the caller, and the
// kind decides whether the message is dropped, retried, or in doubt. The
// 400s and 401s are as the live service answered them.
func TestEveryAnswerHasItsKind(t *testing.T) {
	const sent, refused, later, lost = "sent", "refused", "later", "lost"
	failure := func(code int) string {
		return `{"success":false,"errors":[{"code":` + code2s(code) + `,"message":"echo ana@example.org"}],"result":null}`
	}
	cases := []struct {
		why    string
		status int
		body   string
		kind   string
	}{
		{"a malformed request", 400, failure(10001), refused},
		{"an invalid email: address, header or sender domain", 400, failure(10202), refused},
		{"too big", 400, failure(10200), refused},
		{"no content length", 400, failure(10201), refused},
		{"too big for the edge", 413, failure(10200), refused},
		{"a 400 that is not about the email", 400, failure(6003), later},
		{"a bad token or a wrong account", 401, failure(10000), later},
		{"not entitled", 403, failure(10105), later},
		{"an account id that routes nowhere", 404, failure(7003), later},
		{"any other 4xx", 409, failure(1), later},
		{"the limit", 429, failure(10004), later},
		{"Cloudflare's sign-in down", 503, failure(10100), later},
		{"a 503 from an edge, saying nothing", 503, "<html>unavailable</html>", lost},
		{"an internal error", 500, failure(10002), lost},
		{"a gateway's own page", 502, "<html>bad gateway</html>", lost},
		{"a success that says it failed", 200, failure(10004), later},
		{"a success that names nobody", 200, `{"success":true,"result":{"message_id":"<x@example.com>","queued":[]}}`, lost},
		{"taken, and listed as suppressed", 200, strings.Replace(taken, `"queued":["ana@example.org"]`, `"queued":[]`, 1) + "", sent},
	}
	// The last case lists the recipient under suppressed_recipients.
	cases[len(cases)-1].body = strings.Replace(cases[len(cases)-1].body, `"suppressed_recipients":[]`, `"suppressed_recipients":["ana@example.org"]`, 1)
	for _, c := range cases {
		s := &stand{status: c.status, body: c.body}
		_, err := s.serve(t).Send(context.Background(), message)
		got := lost
		switch {
		case err == nil:
			got = sent
		case errors.Is(err, driver.ErrRejected):
			got = refused
		case errors.Is(err, driver.ErrUnavailable):
			got = later
		}
		if got != c.kind {
			t.Errorf("%s: %s, want %s (%v)", c.why, got, c.kind, err)
		}
		if err != nil && strings.Contains(err.Error(), "ana@example.org") {
			t.Errorf("%s: the error echoes the address: %v", c.why, err)
		}
		// One answer alone is given for a bad email and for a sender
		// domain the account does not know: only it is marked so.
		if unclear := errors.Is(err, driver.ErrSenderUnclear); unclear != strings.Contains(c.body, `"code":10202`) {
			t.Errorf("%s: said to leave the sender's domain open: %v", c.why, unclear)
		}
	}
}

// What Cloudflare answers once the day's allowance is spent is not known.
// Whatever it is, a refusal while the allowance is spent is a reason to
// wait until it resets, never a reason to drop the message.
func TestARefusalWhileTheAllowanceIsSpentIsAWait(t *testing.T) {
	resets := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	spent := `{"success":true,"result":{"quota":{"value":200,"unit":"day"},"usage":{"sent":200,"over_quota":true,"resets_at":"` + resets + `"}}}`
	for _, refusal := range []int{400, 403} {
		s := &stand{status: refusal, body: `{"success":false,"errors":[{"code":10202}],"result":null}`, limits: spent}
		_, err := s.serve(t).Send(context.Background(), message)
		var throttled *driver.Throttled
		if !errors.As(err, &throttled) || throttled.After < 2*time.Hour || throttled.After > 3*time.Hour {
			t.Errorf("a %d with the allowance spent: %v", refusal, err)
		}
	}
	unspent := strings.Replace(spent, `"over_quota":true`, `"over_quota":false`, 1)
	s := &stand{status: 400, body: `{"success":false,"errors":[{"code":10202}],"result":null}`, limits: unspent}
	if _, err := s.serve(t).Send(context.Background(), message); !errors.Is(err, driver.ErrRejected) {
		t.Errorf("a 400 with allowance left: %v", err)
	}
}

func code2s(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTheLimitSaysHowLongToWait(t *testing.T) {
	s := &stand{status: 429, body: `{"success":false,"errors":[{"code":10004}],"result":null}`, header: map[string]string{"Retry-After": "120"}}
	_, err := s.serve(t).Send(context.Background(), message)
	var throttled *driver.Throttled
	if !errors.As(err, &throttled) || throttled.After != 2*time.Minute {
		t.Fatalf("%v", err)
	}
}

// A yes that cannot be read is still a yes: the email was taken, without an
// id, and answering an error would send it twice.
func TestASuccessThatCannotBeReadIsStillSent(t *testing.T) {
	for _, body := range []string{"", "not json", `{"success":true,"result":"odd"}`} {
		s := &stand{status: 200, body: body}
		if id, err := s.serve(t).Send(context.Background(), message); err != nil || id != "" {
			t.Errorf("%q: id %q, err %v", body, id, err)
		}
	}
}

// A failure before any byte could leave sent nothing, and says its cause.
func TestNoConnectionSentNothing(t *testing.T) {
	d := New("acct-1", "token-1")
	d.base = "http://127.0.0.1:1/accounts/acct-1" // nothing listens on port 1
	_, err := d.Send(context.Background(), message)
	if !errors.Is(err, driver.ErrUnavailable) || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("%v", err)
	}
}

func TestNoAnswerIsInDoubt(t *testing.T) {
	s := &stand{hang: true}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.serve(t).Send(ctx, message)
	if err == nil || errors.Is(err, driver.ErrRejected) || errors.Is(err, driver.ErrUnavailable) {
		t.Fatalf("a call that never answered: %v", err)
	}
}

func TestQuotaIsReadAndReused(t *testing.T) {
	s := &stand{status: 200, body: `{"success":true,"result":{"quota":{"value":200,"unit":"day"},
		"usage":{"sent":7,"over_quota":false,"resets_at":"2026-11-13T15:54:45Z"},"recipient_mode":"arbitrary"}}`}
	d := s.serve(t)
	q, err := d.Quota(context.Background())
	if err != nil || q.Limit != 200 || q.Per != "day" || q.Used != 7 || q.Over || q.ResetsAt.IsZero() {
		t.Fatalf("%+v %v", q, err)
	}
	if s.path != "/accounts/acct-1/email/sending/limits" {
		t.Fatalf("asked at %s", s.path)
	}
	d.Quota(context.Background())
	if s.calls != 1 {
		t.Fatalf("Cloudflare was asked %d times for a fresh answer", s.calls)
	}
}

func TestAQuotaThatCannotBeReadIsAnErrorAndIsNotKept(t *testing.T) {
	s := &stand{status: 403, body: `{"success":false,"errors":[{"code":10102}],"result":null}`}
	d := s.serve(t)
	if _, err := d.Quota(context.Background()); !errors.Is(err, driver.ErrUnavailable) {
		t.Fatalf("a refusal: %v", err)
	}
	// Whoever asks right after is told the same, and Cloudflare is left
	// alone; once that time is over it is asked again.
	if _, err := d.Quota(context.Background()); !errors.Is(err, driver.ErrUnavailable) || s.calls != 1 {
		t.Fatalf("asked again at once: %v, %d calls", err, s.calls)
	}
	later := func() { d.quota.failedAt = d.quota.failedAt.Add(-quotaRetry) }
	later()
	// A caller that left is no failure to remember.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Quota(gone); err == nil {
		t.Fatal("a call nobody waited for answered")
	}
	if d.quota.failed != nil && time.Since(d.quota.failedAt) < quotaRetry {
		t.Fatal("a caller that left was remembered as a failure")
	}
	for _, body := range []string{`{"success":false,"result":null}`, `{"success":true,"result":null}`, `{"success":true,"result":{}}`} {
		s.status, s.body = 200, body
		if q, err := d.Quota(context.Background()); err == nil {
			t.Errorf("%s was read as %+v", body, q)
		}
		later()
	}
	// A reset time Cloudflare leaves empty is no reason to fail the rest.
	s.body = `{"success":true,"result":{"quota":{"value":200,"unit":"day"},"usage":{"sent":0,"over_quota":false,"resets_at":""}}}`
	if q, err := d.Quota(context.Background()); err != nil || q.Limit != 200 || !q.ResetsAt.IsZero() {
		t.Fatalf("an empty reset time: %+v %v", q, err)
	}
}
