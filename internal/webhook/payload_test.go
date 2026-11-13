package webhook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
)

func TestAWebhookSaysWhatHappenedAndWhetherTheAddressIsAtFault(t *testing.T) {
	m := store.Message{ID: "msg-1", Sender: "app@example.com", Recipient: `"ana maria"@example.org`, CreatedAt: at}
	for name, c := range map[string]struct {
		event      store.Event
		wantType   string
		bounce     string
		suppressed bool
	}{
		"delivered":       {store.Event{Kind: store.EventDelivered}, "email.delivered", "", false},
		"delayed":         {store.Event{Kind: store.EventDeferred, Code: "4.2.2"}, "email.delivery_delayed", "", false},
		"a hard bounce":   {store.Event{Kind: store.EventBounced, Permanent: true, Code: "5.1.1", Reason: "permanent_failure", Suppressed: true}, "email.bounced", "Permanent", true},
		"a soft bounce":   {store.Event{Kind: store.EventBounced, Code: "4.2.2"}, "email.bounced", "Transient", false},
		"a refusal of us": {store.Event{Kind: store.EventBounced, Permanent: true, Code: "5.7.26"}, "email.bounced", "Policy", false},
		"a complaint":     {store.Event{Kind: store.EventComplained, Suppressed: true}, "email.complained", "", true},
		"failed":          {store.Event{Kind: store.EventFailed, Reason: "policy"}, "email.failed", "", false},
	} {
		c.event.At = at
		raw := body(c.event, m)
		var got struct {
			Type      string `json:"type"`
			CreatedAt string `json:"created_at"`
			Data      struct {
				EmailID    string   `json:"email_id"`
				From       string   `json:"from"`
				To         []string `json:"to"`
				Code       string   `json:"code"`
				Reason     string   `json:"reason"`
				Suppressed *bool    `json:"suppressed"`
				Bounce     *struct {
					Type string `json:"type"`
				} `json:"bounce"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: %v in %s", name, err, raw)
		}
		bounce := ""
		if got.Data.Bounce != nil {
			bounce = got.Data.Bounce.Type
		}
		if got.Type != c.wantType || bounce != c.bounce || got.Data.Suppressed == nil || *got.Data.Suppressed != c.suppressed ||
			got.Data.EmailID != "msg-1" || got.Data.From != "app@example.com" || len(got.Data.To) != 1 ||
			got.Data.To[0] != m.Recipient || got.Data.Code != c.event.Code || got.Data.Reason != c.event.Reason ||
			got.CreatedAt != "2026-01-02T03:04:05Z" {
			t.Errorf("%s: %s", name, raw)
		}
		if strings.Contains(string(raw), "subject") || strings.HasSuffix(string(raw), "\n") {
			t.Errorf("%s: %s", name, raw)
		}
	}
	// A bounce for good that left the address off the list, because its
	// suppression had been lifted, is still a bounce for good.
	lifted := string(body(store.Event{Kind: store.EventBounced, Permanent: true, Code: "5.1.1", At: at}, m))
	if !strings.Contains(lifted, `"bounce":{"type":"Permanent"},"suppressed":false`) {
		t.Errorf("a bounce after a lift: %s", lifted)
	}
	// Every kind of event has a type, and a type names one kind.
	for kind := range store.StateOf {
		if TypeOf(kind) == "" || kindOf(TypeOf(kind)) != kind {
			t.Errorf("kind %q has no type of its own", kind)
		}
	}
	if len(Types()) != len(store.StateOf) || kindOf(TypeTest) != "" {
		t.Errorf("types: %v", Types())
	}
	if string(testBody(at)) != `{"created_at":"2026-01-02T03:04:05Z","data":{},"type":"webhook.test"}` {
		t.Errorf("the test webhook: %s", testBody(at))
	}
}
