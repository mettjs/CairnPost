package api_test

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// at is the standing clock of a rig made by laterRig.
var at = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

// laterRig is the API over a clock that stands still, with what may
// wait bounded.
func laterRig(t *testing.T, room int64, keep time.Duration) *rig {
	s := memstore.New()
	s.Now = func() time.Time { return at }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	drv := &fake{}
	r := newRigBuilt(t, s, func(d *api.Deps) {
		d.Mail = mail.New(s, drv, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour, InDoubtWindow: time.Minute,
			Now: s.Now, ScheduleRoom: room, KeepContent: keep}, log)
	})
	r.drv = drv
	return r
}

func later(to, when string) string {
	return fmt.Sprintf(`{"from":"App <app@example.com>","to":%q,"subject":"SUBJECTMARKQ","text":"Body text","scheduled_at":%q}`, to, when)
}

func TestAnEmailAskedForAheadWaitsAndIsNotSent(t *testing.T) {
	r := laterRig(t, 0, 0)
	code, _, sent := r.do("POST", "/emails", r.send, "later-1", later("ana@example.org", "2030-01-03T09:00:00+01:00"))
	if code != 200 || sent["id"] == "" || sent["scheduled_at"] != "2030-01-03T08:00:00Z" || len(r.drv.held) != 0 {
		t.Fatalf("asked for: %d %v, %d sent", code, sent, len(r.drv.held))
	}
	id := sent["id"].(string)
	// The same request again is that email; another under the key is not.
	if _, _, again := r.do("POST", "/emails", r.send, "later-1", later("ana@example.org", "2030-01-03T09:00:00+01:00")); again["id"] != id || again["scheduled_at"] != "2030-01-03T08:00:00Z" {
		t.Fatalf("asked again: %v", again)
	}
	if code, name, _ := r.do("POST", "/emails", r.send, "later-1", later("ana@example.org", "2030-01-03T10:00:00Z")); code != 409 || name != "invalid_idempotent_request" {
		t.Fatalf("another time under the key: %d %s", code, name)
	}
	code, _, read := r.do("GET", "/emails/"+id, r.send, "", "")
	if code != 200 || read["state"] != "scheduled" || read["scheduled_at"] != "2030-01-03T08:00:00Z" || read["to"] != "ana@example.org" || read["content"] != nil {
		t.Fatalf("read by its sender: %d %v", code, read)
	}
	// What it says is kept until it is sent, and shown to an admin key.
	_, _, read = r.do("GET", "/emails/"+id, r.admin, "", "")
	if content, _ := read["content"].(map[string]any); content["subject"] != "SUBJECTMARKQ" || content["kept_until"] != "2030-01-03T08:00:00Z" {
		t.Fatalf("read by an admin: %v", read)
	}
}

func TestATimeAnEmailCannotBeSentAtIsRefused(t *testing.T) {
	r := laterRig(t, 0, 0)
	for _, when := range []string{"tomorrow", "2030-01-02T03:04:05Z", "2029-01-01T00:00:00Z", "2030-02-02T03:04:06Z", "2030-01-03 09:00"} {
		if code, name, body := r.do("POST", "/emails", r.send, "", later("ana@example.org", when)); code != 422 || name != "validation_error" ||
			!strings.Contains(body["message"].(string), "scheduled_at") {
			t.Errorf("%q: %d %s %v", when, code, name, body)
		}
	}
	if len(r.drv.held) != 0 {
		t.Errorf("%d sent", len(r.drv.held))
	}
}

func TestAWaitingEmailIsCancelledOrMovedByWhoMay(t *testing.T) {
	r := laterRig(t, 0, 0)
	_, _, sent := r.do("POST", "/emails", r.send, "", later("ana@example.org", "2030-01-03T08:00:00Z"))
	id := sent["id"].(string)
	code, _, moved := r.do("PATCH", "/emails/"+id, r.send, "", `{"scheduled_at":"2030-01-05T08:00:00Z"}`)
	if code != 200 || moved["scheduled_at"] != "2030-01-05T08:00:00Z" {
		t.Fatalf("moved: %d %v", code, moved)
	}
	for body, want := range map[string]string{`{}`: "validation_error", `{"scheduled_at":"2020-01-01T00:00:00Z"}`: "validation_error",
		`{"subject":"x"}`: "validation_error"} {
		if code, name, _ := r.do("PATCH", "/emails/"+id, r.send, "", body); code != 422 || name != want {
			t.Errorf("%s: %d %s", body, code, name)
		}
	}
	// An admin may; a key that neither sent it nor is an admin's is told
	// there is no such email, as is anyone asking for one there is not.
	stranger := otherSendKey(t, r)
	for _, path := range []string{"/emails/" + id + "/cancel", "/emails/nope/cancel"} {
		who := stranger
		if strings.Contains(path, "nope") {
			who = r.admin
		}
		if code, name, _ := r.do("POST", path, who, "", ""); code != 404 || name != "not_found" {
			t.Errorf("%s: %d %s", path, code, name)
		}
	}
	code, _, gone := r.do("POST", "/emails/"+id+"/cancel", r.admin, "", "")
	if code != 200 || gone["state"] != "cancelled" {
		t.Fatalf("cancelled: %d %v", code, gone)
	}
	_, _, read := r.do("GET", "/emails/"+id, r.admin, "", "")
	if read["state"] != "cancelled" || read["content"] != nil || read["scheduled_at"] != "2030-01-05T08:00:00Z" {
		t.Fatalf("read once cancelled: %v", read)
	}
	for _, again := range [][2]string{{"POST", "/emails/" + id + "/cancel"}, {"PATCH", "/emails/" + id}} {
		if code, name, _ := r.do(again[0], again[1], r.send, "", `{"scheduled_at":"2030-01-06T08:00:00Z"}`); code != 409 || name != "not_scheduled" {
			t.Errorf("%s %s once cancelled: %d %s", again[0], again[1], code, name)
		}
	}
	// An email that was sent does not wait.
	_, _, now := r.do("POST", "/emails", r.send, "", good)
	if code, name, _ := r.do("POST", "/emails/"+now["id"].(string)+"/cancel", r.send, "", ""); code != 409 || name != "not_scheduled" {
		t.Errorf("cancelling a sent email: %d %s", code, name)
	}
}

func TestOnlySoMuchMayWait(t *testing.T) {
	r := laterRig(t, 100, 0)
	if code, _, _ := r.do("POST", "/emails", r.send, "", later("ana@example.org", "2030-01-03T08:00:00Z")); code != 200 {
		t.Fatalf("the first: %d", code)
	}
	if code, name, _ := r.do("POST", "/emails", r.send, "", later("ben@example.org", "2030-01-03T08:00:00Z")); code != 503 || name != "schedule_full" {
		t.Fatalf("the second: %d %s", code, name)
	}
}

func TestABatchMaySendSomeNowAndSomeLater(t *testing.T) {
	r := laterRig(t, 0, 0)
	code, _, body := r.do("POST", "/emails/batch", r.send, "", "["+email("ana@example.org")+","+later("ben@example.org", "2030-01-03T08:00:00Z")+"]")
	got := items(t, body)
	if code != 200 || got[0]["scheduled_at"] != nil || got[1]["scheduled_at"] != "2030-01-03T08:00:00Z" || len(r.drv.held) != 1 {
		t.Fatalf("%d %v, %d sent", code, body, len(r.drv.held))
	}
}
