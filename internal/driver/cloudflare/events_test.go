package cloudflare

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
)

// The bodies are shaped as the live service's were, with every id, domain
// and address replaced.
const (
	delivered = `{"type":"cf.email.sending.message.delivered","source":{"zoneId":"zone","domain":"mail.example.com","type":"email.sending"},"metadata":{"accountId":"acct","eventSubscriptionId":"sub","eventSchemaVersion":1,"eventTimestamp":"2026-01-02T03:04:05.262Z"},"payload":{"eventId":"evt-1","messageId":"<abc@mail.example.com>","sender":"app@mail.example.com","recipient":"ana@example.org","subject":"SUBJECTMARK","terminal":true,"delivery":{"status":"delivered","provider":"gmail","deliveryTimeMs":706}}}`
	bounced   = `{"type":"cf.email.sending.message.bounced","metadata":{"eventTimestamp":"2026-01-02T03:04:06Z"},"payload":{"eventId":"evt-2","messageId":"<def@mail.example.com>","recipient":"gone@example.org","subject":"SUBJECTMARK","terminal":true,"delivery":{"status":"bounced","provider":"gmail","smtpStatusCode":"550","smtpEnhancedStatusCode":"5.1.1","smtpResponse":"permanent error (550): 5.1.1 no such user"},"bounce":{"type":"hard","classification":"permanent_failure","reason":"permanent error (550): 5.1.1 no such user"}}}`
)

func TestEventsAreReadAsTheServiceSendsThem(t *testing.T) {
	e, ok := read(delivered)
	if !ok || e.ID != "evt-1" || e.UpstreamID != "<abc@mail.example.com>" || e.Kind != driver.Delivered || e.Permanent ||
		!e.At.Equal(time.Date(2026, 1, 2, 3, 4, 5, 262e6, time.UTC)) {
		t.Fatalf("delivered: %+v %v", e, ok)
	}
	e, ok = read(bounced)
	if !ok || e.Kind != driver.Bounced || !e.Permanent || e.Code != "5.1.1" || e.Reason != "permanent_failure" {
		t.Fatalf("a hard bounce: %+v %v", e, ok)
	}
	// A queue may hand JSON over base64-encoded.
	if e, ok := read(base64.StdEncoding.EncodeToString([]byte(delivered))); !ok || e.ID != "evt-1" {
		t.Fatalf("base64: %+v %v", e, ok)
	}
}

// The shapes below are from Cloudflare's documentation; none was seen live.
func TestTheOtherKindsAsDocumented(t *testing.T) {
	for name, want := range map[string]struct {
		kind      string
		permanent bool
	}{
		"deferred": {driver.Deferred, false}, "complained": {driver.Complained, false},
		"failed": {driver.Failed, false}, "rejected": {driver.Failed, false},
	} {
		body := `{"type":"cf.email.sending.message.` + name + `","metadata":{"eventTimestamp":"2026-01-02T03:04:06Z"},
			"payload":{"eventId":"e","messageId":"<m@example.com>","bounce":{"type":"soft","reason":"mailbox full"},
			"rejection":{"reason":"suppressed"}}}`
		if e, ok := read(body); !ok || e.Kind != want.kind || e.Permanent != want.permanent {
			t.Errorf("%s: %+v %v", name, e, ok)
		}
	}
	soft := strings.Replace(bounced, `"type":"hard"`, `"type":"soft"`, 1)
	if e, _ := read(soft); e.Permanent {
		t.Error("a soft bounce was read as permanent")
	}
}

func TestWhatIsNoEventAboutAnEmailIsNotRead(t *testing.T) {
	for name, body := range map[string]string{
		"not json":          "hello",
		"another product":   `{"type":"cf.r2.object.created","payload":{"eventId":"e","messageId":"m"}}`,
		"an unknown kind":   `{"type":"cf.email.sending.message.opened","payload":{"eventId":"e","messageId":"m"}}`,
		"no message id":     `{"type":"cf.email.sending.message.delivered","payload":{"eventId":"e"}}`,
		"no event id":       `{"type":"cf.email.sending.message.delivered","payload":{"messageId":"m"}}`,
		"a look-alike type": `{"type":"delivered","payload":{"eventId":"e","messageId":"m"}}`,
	} {
		if e, ok := read(body); ok {
			t.Errorf("%s was read as %+v", name, e)
		}
	}
}

func TestAnEventKeepsNothingOfTheSubject(t *testing.T) {
	e, _ := read(delivered)
	b, _ := read(bounced)
	for _, field := range []string{e.ID, e.UpstreamID, e.Kind, e.Code, e.Reason, b.ID, b.UpstreamID, b.Code, b.Reason} {
		// Neither the subject nor the receiving server's sentence, which
		// can quote the email, is read into an event.
		if strings.Contains(field, "SUBJECTMARK") || strings.Contains(field, "no such user") {
			t.Fatalf("an event holds the subject or a server's words: %q", field)
		}
	}
}

// Four of the six kinds were never seen live. An event is not thrown away
// because a field nobody needs has another shape than expected: only what
// names the event is required.
func TestAnEventIsReadWhateverItsOptionalFieldsLookLike(t *testing.T) {
	const head = `{"type":"cf.email.sending.message.bounced","payload":{"eventId":"e","messageId":"<m@example.com>",`
	for name, rest := range map[string]string{
		"a numeric status":        `"delivery":{"smtpStatusCode":550}}}`,
		"a failure as a string":   `"failure":"it broke"}}`,
		"a bounce as a string":    `"bounce":"hard"}}`,
		"no metadata at all":      `"delivery":{}}}`,
		"a time that is no time":  `"delivery":{}},"metadata":{"eventTimestamp":"yesterday"}}`,
		"a status that is a text": `"delivery":{"smtpEnhancedStatusCode":"see https://example.org ana@example.org"}}}`,
		"a reason that is a text": `"bounce":{"type":"hard","classification":"The user ana@example.org is gone"}}}`,
		"a NUL in a field":        `"bounce":{"classification":"perm\u0000anent"}}}`,
	} {
		e, ok := read(head + rest)
		if !ok || e.ID != "e" || e.Kind != driver.Bounced {
			t.Errorf("%s: %+v %v", name, e, ok)
		}
		if strings.ContainsAny(e.Code+e.Reason, " @\x00") {
			t.Errorf("%s: a sentence or a control character was kept: %q %q", name, e.Code, e.Reason)
		}
	}
	if e, _ := read(head + `"delivery":{"smtpStatusCode":550}}}`); e.Code != "550" {
		t.Errorf("a numeric status was read as %q", e.Code)
	}
}

// An id that could not be kept, or is missing, makes the event unreadable,
// and it still says what kind it was.
func TestAnEventThatCannotBeNamedSaysItsKind(t *testing.T) {
	for name, body := range map[string]string{
		"no event id":      `{"type":"cf.email.sending.message.complained","payload":{"messageId":"m"}}`,
		"no message id":    `{"type":"cf.email.sending.message.complained","payload":{"eventId":"e"}}`,
		"a NUL in the id":  `{"type":"cf.email.sending.message.complained","payload":{"eventId":"e\u0000","messageId":"m"}}`,
		"an id too long":   `{"type":"cf.email.sending.message.complained","payload":{"eventId":"` + strings.Repeat("x", 300) + `","messageId":"m"}}`,
		"a space in an id": `{"type":"cf.email.sending.message.complained","payload":{"eventId":"e","messageId":"a b"}}`,
	} {
		if e, ok := read(body); ok || e.Kind != driver.Complained || e.ID != "" {
			t.Errorf("%s: %+v %v", name, e, ok)
		}
	}
}

func TestTakeAndSettleSpeakTheQueuesLanguage(t *testing.T) {
	timeless := strings.Replace(delivered, `"eventTimestamp":"2026-01-02T03:04:05.262Z"`, `"eventTimestamp":""`, 1)
	bodyJSON := `{"success":true,"result":{"message_backlog_count":1,"messages":[{"id":"q1","body":` +
		strconv.Quote(timeless) + `,"attempts":2,"timestamp_ms":1767323045000,"lease_id":"lease-1"},{"id":"q2","body":"junk","lease_id":"lease-2"}]}}`
	s := &stand{status: 200, body: bodyJSON}
	events := s.serve(t).Events("queue-1")
	got, err := events.Take(context.Background(), 50, 90*time.Second)
	if err != nil || len(got) != 2 || !got[0].Readable || got[0].Lease != "lease-1" || got[0].Attempts != 2 ||
		got[1].Readable || got[1].Lease != "lease-2" {
		t.Fatalf("%+v %v", got, err)
	}
	// An event that does not say when it happened takes the queue's stamp.
	if !got[0].Event.At.Equal(time.UnixMilli(1767323045000)) {
		t.Fatalf("the time: %s", got[0].Event.At)
	}
	if s.path != "/accounts/acct-1/queues/queue-1/messages/pull" || s.request["batch_size"] != 50.0 ||
		s.request["visibility_timeout_ms"] != 90000.0 {
		t.Fatalf("pull: %s %v", s.path, s.request)
	}
	s.body = `{"success":true,"result":{"ackCount":1,"retryCount":1}}`
	if err := events.Settle(context.Background(), []string{"lease-2"}, []string{"lease-1"}, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	acks, _ := s.request["acks"].([]any)
	retries, _ := s.request["retries"].([]any)
	if s.path != "/accounts/acct-1/queues/queue-1/messages/ack" || len(acks) != 1 || len(retries) != 1 ||
		retries[0].(map[string]any)["delay_seconds"] != 15.0 {
		t.Fatalf("ack: %s %v", s.path, s.request)
	}
	before := s.calls
	if err := events.Settle(context.Background(), nil, nil, 0); err != nil || s.calls != before {
		t.Fatalf("settling nothing called the queue: %v", err)
	}
}

// A queue that refuses is an error, never an empty answer, and the error
// says what to check. Measured: a wrong queue id answers 404, a bad token 401.
func TestAQueueThatRefusesSaysWhatToCheck(t *testing.T) {
	for status, hint := range map[int]string{404: "CAIRNPOST_CLOUDFLARE_QUEUE_ID", 401: "Queues permission", 403: "Queues permission"} {
		s := &stand{status: status, body: `{"success":false,"errors":[{"message":"Queue does not exist"}],"result":null}`}
		got, err := s.serve(t).Events("q").Take(context.Background(), 1, time.Minute)
		if err == nil || !strings.Contains(err.Error(), hint) || strings.Contains(err.Error(), "mail") {
			t.Errorf("a %d was read as %v, %v", status, got, err)
		}
	}
	s := &stand{status: 429, body: `{"success":false,"errors":[],"result":null}`, header: map[string]string{"Retry-After": "40"}}
	_, err := s.serve(t).Events("q").Take(context.Background(), 1, time.Minute)
	var throttled *driver.Throttled
	if !errors.As(err, &throttled) || throttled.After != 40*time.Second {
		t.Errorf("a 429: %v", err)
	}
}

// A pull's answer can be far larger than a send's.
func TestALargeBatchIsRead(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	s := &stand{status: 200, body: `{"success":true,"result":{"messages":[{"body":"` + big + `","lease_id":"l"}]}}`}
	got, err := s.serve(t).Events("q").Take(context.Background(), 1, time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("%d items, %v", len(got), err)
	}
}
