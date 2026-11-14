package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

// contentRig is the API over a store on a clock the test moves, keeping
// what emails say for keep.
func contentRig(t *testing.T, s *memstore.Store, keep time.Duration) *rig {
	return newRigBuilt(t, s, func(d *api.Deps) {
		d.Mail = mail.New(s, &fake{}, mail.Options{SendTimeout: time.Second, IdempotencyTTL: time.Hour,
			InDoubtWindow: time.Minute, KeepContent: keep}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
}

const worded = `{"from":"Billing <app@example.com>","to":"ana@example.org","reply_to":"help@example.com",
	"subject":"Your code is 4821","text":"Use 4821","html":"<b>4821</b>","headers":{"X-Trace":"t-1"},
	"attachments":[{"filename":"receipt.pdf","content":"JVBERi0x"}]}`

// What an email said is kept only where the operator chose to keep it, and
// shown only to an admin key that may see the sending domain, only while
// it is kept.
func TestWhatAnEmailSaidIsShownToAnAdminWhileItIsKept(t *testing.T) {
	s := memstore.New()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	r := contentRig(t, s, 24*time.Hour)
	_, _, sent := r.do("POST", "/emails", r.send, "", worded)
	path := "/emails/" + sent["id"].(string)

	_, _, got := r.do("GET", path, r.admin, "", "")
	c, _ := got["content"].(map[string]any)
	if c["subject"] != "Your code is 4821" || c["text"] != "Use 4821" || c["html"] != "<b>4821</b>" ||
		c["from_name"] != "Billing" || c["reply_to"] != "help@example.com" || c["kept_until"] != "2030-01-02T00:00:00Z" ||
		c["headers"].(map[string]any)["X-Trace"] != "t-1" {
		t.Fatalf("an admin key was shown %v", got["content"])
	}
	// Of a file, what it was: never its bytes.
	files, _ := c["attachments"].([]any)
	if file, _ := files[0].(map[string]any); len(files) != 1 || file["filename"] != "receipt.pdf" || file["content_type"] != "application/pdf" ||
		file["size"] != float64(6) || file["content"] != nil {
		t.Fatalf("the files shown: %v", files)
	}
	// The application's own key is not: the setting is for the operator.
	if code, _, got := r.do("GET", path, r.send, "", ""); code != 200 || got["content"] != nil {
		t.Fatalf("a send key was shown %d %v", code, got["content"])
	}
	// An admin key is, also for an email it sent itself.
	_, _, mine := r.do("POST", "/emails", r.admin, "", worded)
	if _, _, got := r.do("GET", "/emails/"+mine["id"].(string), r.admin, "", ""); got["content"] == nil {
		t.Fatal("an admin key was not shown what it sent")
	}
	// An admin key bound to another sending domain is not; one bound to
	// this one is.
	for domain, shown := range map[string]bool{"other.example": false, "example.com": true} {
		bound, _, err := keys.New(s).Create(context.Background(), "bound", store.ScopeAdmin, domain)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, got := r.do("GET", path, bound, "", ""); (got["content"] != nil) != shown {
			t.Fatalf("an admin key bound to %s: shown is %v", domain, got["content"] != nil)
		}
	}
	// Past its time it is not shown, pruned yet or not.
	now = now.Add(24 * time.Hour)
	if code, _, got := r.do("GET", path, r.admin, "", ""); code != 200 || got["content"] != nil {
		t.Fatalf("past its time: %d %v", code, got["content"])
	}
}

// With the setting off, nothing is kept; and turning it off hides at once
// what was kept while it was on.
func TestContentIsNotKeptUnlessTheOperatorChoseTo(t *testing.T) {
	s := memstore.New()
	off := contentRig(t, s, 0)
	_, _, sent := off.do("POST", "/emails", off.send, "", worded)
	if raw := off.raw("GET", "/emails/"+sent["id"].(string), off.admin); strings.Contains(raw, "4821") || strings.Contains(raw, "content") {
		t.Fatalf("with the setting off: %s", raw)
	}
	if raw, _, _ := s.Content(context.Background(), sent["id"].(string), time.Hour); raw != nil {
		t.Fatal("with the setting off, the store was handed the email's words")
	}

	on := contentRig(t, s, time.Hour)
	_, _, sent = on.do("POST", "/emails", on.send, "", worded)
	if raw := on.raw("GET", "/emails/"+sent["id"].(string), on.admin); !strings.Contains(raw, "4821") {
		t.Fatalf("with the setting on: %s", raw)
	}
	// The same store, the setting turned off again. The keys are the
	// store's, so the rig's new ones read what the old ones sent.
	again := contentRig(t, s, 0)
	if raw := again.raw("GET", "/emails/"+sent["id"].(string), again.admin); strings.Contains(raw, "4821") {
		t.Fatalf("turned off again: %s", raw)
	}
}

// What a receiving server said of a bounce is kept under the same rule:
// shown to an admin key while it is kept, to no other key, and never
// handed to a webhook.
func TestWhatAServerSaidIsKeptLikeTheEmailsWords(t *testing.T) {
	ctx, s := context.Background(), memstore.New()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	r := contentRig(t, s, 24*time.Hour)
	if _, err := s.CreateEndpoint(ctx, store.Endpoint{URL: "https://example.com/hook", Enabled: true}, 10); err != nil {
		t.Fatal(err)
	}
	_, _, sent := r.do("POST", "/emails", r.send, "", worded)
	id := sent["id"].(string)
	if err := s.Sent(ctx, id, "<up>"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordEvent(ctx, store.Event{Driver: "fake", UpstreamEventID: "e1", UpstreamID: "<up>", Kind: store.EventBounced,
		Permanent: true, Code: "5.1.1", At: now, Detail: "550 no such user here", KeepDetail: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if raw := r.raw("GET", "/emails/"+id, r.admin); !strings.Contains(raw, `"detail":"550 no such user here"`) {
		t.Fatalf("an admin key: %s", raw)
	}
	if raw := r.raw("GET", "/emails/"+id, r.send); strings.Contains(raw, "no such user") {
		t.Fatalf("a send key: %s", raw)
	}
	due, err := s.TakeDeliveries(ctx, 10, 2, nil, time.Minute)
	if err != nil || len(due) != 1 || due[0].Event.Detail != "" {
		t.Fatalf("a webhook was handed %+v: %v", due, err)
	}
	now = now.Add(24 * time.Hour)
	if raw := r.raw("GET", "/emails/"+id, r.admin); strings.Contains(raw, "no such user") {
		t.Fatalf("past its time: %s", raw)
	}
	if n, err := s.PruneDetails(ctx, 10); err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	// A sentence that comes after its email's words are gone is not kept:
	// it could quote them.
	if _, err := s.RecordEvent(ctx, store.Event{Driver: "fake", UpstreamEventID: "e2", UpstreamID: "<up>", Kind: store.EventBounced,
		At: now, Detail: "said too late", KeepDetail: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if said, err := s.Details(ctx, id, 1000*time.Hour); err != nil || len(said) != 0 {
		t.Fatalf("kept after its time: %v %v", said, err)
	}
}
