package api_test

import (
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/mail"
)

// A refusal that is most likely the sending domain's says so, names a
// record that was looked for and how to check, and keeps "likely": only
// DNS was read. The email's id comes with it, as with any send that was
// recorded and then failed.
func TestARefusalLikelyTheDomainsSaysWhatToDo(t *testing.T) {
	r := newRig(t)
	r.drv.err = &mail.SenderDomain{Domain: "example.com", Record: mail.Record{Name: "cf-bounce._domainkey.example.com", Type: "TXT"}}
	code, name, body := r.do("POST", "/emails", r.send, "", good)
	message, _ := body["message"].(string)
	if id, _ := body["id"].(string); code != 422 || name != "sender_domain_not_ready" || id == "" {
		t.Fatalf("%d %v", code, body)
	}
	for _, want := range []string{"example.com is likely not set up", "none of the DNS records", "cf-bounce._domainkey.example.com, a TXT record",
		`"cairnpost domains check example.com"`, "Nothing was sent", "same Idempotency-Key"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message lacks %q: %s", want, message)
		}
	}
}

// In a batch the email from such a domain carries the same name.
func TestABatchNamesAnEmailWhoseDomainIsNotSetUp(t *testing.T) {
	r := newRig(t)
	r.drv.refuse = map[string]error{"ben@example.org": &mail.SenderDomain{Domain: "example.com"}}
	code, _, body := r.do("POST", "/emails/batch", r.send, "", "["+email("ana@example.org")+","+email("ben@example.org")+"]")
	got := items(t, body)
	if code != 207 || len(got) != 2 || len(r.drv.held) != 1 {
		t.Fatalf("%d %v", code, body)
	}
	if refused, _ := got[1]["error"].(map[string]any); refused["name"] != "sender_domain_not_ready" {
		t.Fatalf("the second email: %v", got[1])
	}
}
