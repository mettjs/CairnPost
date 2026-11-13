package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/address"
	"github.com/mettjs/cairnpost/internal/driver"
)

// kinds maps Cloudflare's event names, after their common prefix, to ours.
// "rejected" is Cloudflare refusing by its own policy after taking the
// email: to the sender, an email that failed.
var kinds = map[string]string{
	"delivered": driver.Delivered, "deferred": driver.Deferred, "bounced": driver.Bounced,
	"complained": driver.Complained, "failed": driver.Failed, "rejected": driver.Failed,
}

const prefix = "cf.email.sending.message."

var (
	// An id is what Cloudflare makes: printable, no spaces, not long.
	idShape = regexp.MustCompile(`^[\x21-\x7E]{1,255}$`)
	// A status is "550" or "5.1.1".
	codeShape = regexp.MustCompile(`^[0-9]{3}$|^[0-9]\.[0-9]{1,3}\.[0-9]{1,3}$`)
	// A reason is a short machine name: "permanent_failure".
	reasonShape = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
)

// read turns a queue message's body into an event. The body is the event
// as JSON; a queue may hand JSON over base64-encoded.
//
// Only what names the event is required: its type, its id and its email's
// id. Everything else is read if it has the shape expected and left out if
// not, because four of the six kinds were never seen live and an event must
// not be thrown away over a field nobody needs. The email's subject, and
// the receiving server's own sentences, which can quote the email, are
// never read at all.
func read(body string) (driver.Event, bool) {
	var envelope struct {
		Type     string                     `json:"type"`
		Payload  map[string]json.RawMessage `json:"payload"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil || json.Unmarshal(raw, &envelope) != nil {
			return driver.Event{}, false
		}
	}
	kind, known := kinds[strings.TrimPrefix(envelope.Type, prefix)]
	if !known || !strings.HasPrefix(envelope.Type, prefix) {
		return driver.Event{}, false
	}
	e := driver.Event{Kind: kind, ID: text(envelope.Payload["eventId"]), UpstreamID: text(envelope.Payload["messageId"])}
	if !idShape.MatchString(e.ID) || !idShape.MatchString(e.UpstreamID) {
		// An event about an email, which cannot be named: Kind says so.
		return driver.Event{Kind: kind}, false
	}
	delivery, bounce := object(envelope.Payload["delivery"]), object(envelope.Payload["bounce"])
	for _, code := range []string{text(delivery["smtpEnhancedStatusCode"]), text(delivery["smtpStatusCode"])} {
		if e.Code == "" && codeShape.MatchString(code) {
			e.Code = code
		}
	}
	// Measured: a hard bounce says type "hard", classification
	// "permanent_failure".
	e.Permanent = kind == driver.Bounced && text(bounce["type"]) == "hard"
	for _, reason := range []string{text(bounce["classification"]), text(object(envelope.Payload["rejection"])["reason"]),
		text(object(envelope.Payload["complaint"])["type"])} {
		if e.Reason == "" && reasonShape.MatchString(reason) {
			e.Reason = reason
		}
	}
	if at, err := time.Parse(time.RFC3339, text(envelope.Metadata["eventTimestamp"])); err == nil {
		e.At = at.UTC()
	}
	e.Sender, e.Recipient = bareAddress(envelope.Payload["sender"]), bareAddress(envelope.Payload["recipient"])
	return e, true
}

// bareAddress is a JSON value as a bare email address, "" for anything else.
func bareAddress(raw json.RawMessage) string {
	bare, _ := address.Bare(text(raw))
	return bare
}

// text is a JSON value as a string: a string as it is, a number as its
// digits, anything else as "".
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// object is a JSON value as an object, empty for anything else.
func object(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	return m
}
