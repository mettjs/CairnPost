package webhook

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// typeOf is the name each kind of thing an endpoint is owed goes out
// under: every kind of event, and a suppression told on its own.
var typeOf = map[string]string{
	store.KindSuppressed: TypeSuppressed,
	store.KindReceived:   TypeReceived,
	store.EventDelivered: "email.delivered", store.EventDeferred: "email.delivery_delayed",
	store.EventBounced: "email.bounced", store.EventComplained: "email.complained",
	store.EventFailed: "email.failed",
}

// TypeSuppressed is the type of the webhook that tells of an address put
// on the suppression list because of a bounce or a complaint about an
// email this server has no record of. Such an email has no event to say
// so; one that has a record says it in its own webhook ("suppressed").
const TypeSuppressed = "address.suppressed"

// TypeReceived is the type of the webhook that tells of an email that
// came for an address of a sending domain. It says who from, who for
// and when, and the id to read it by: never what it says.
const TypeReceived = "email.received"

// TypeTest is the type of the webhook sent to try an endpoint out.
const TypeTest = "webhook.test"

// Types is every type an endpoint can ask for, in order.
func Types() []string {
	out := make([]string, 0, len(typeOf))
	for _, t := range typeOf {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// TypeOf is the type a kind of event goes out under.
func TypeOf(kind string) string { return typeOf[kind] }

// kindOf is the kind of event a type names, "" for none.
func kindOf(eventType string) string {
	for kind, t := range typeOf {
		if t == eventType {
			return kind
		}
	}
	return ""
}

// How a bounce is typed. Permanent means the address itself will never
// take mail. Policy means the receiving server refused for good for a
// reason that is the sender's (a failed signature, a blocklist): nothing
// is wrong with the address, and nobody should be asked to change it.
const (
	bouncePermanent = "Permanent"
	bouncePolicy    = "Policy"
	bounceTransient = "Transient"
)

type payload struct {
	Type string `json:"type"`
	// CreatedAt is when the event happened.
	CreatedAt time.Time `json:"created_at"`
	Data      *data     `json:"data"`
}

// data is the email the event is about. Never its subject or its body.
type data struct {
	EmailID   string    `json:"email_id"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	CreatedAt time.Time `json:"created_at"`
	// Code is the receiving server's status, Reason the provider's short
	// name for what happened.
	Code   string  `json:"code,omitempty"`
	Reason string  `json:"reason,omitempty"`
	Bounce *bounce `json:"bounce,omitempty"`
	// Suppressed says that, with this event, the address is on the sending
	// domain's suppression list: CairnPost will not send to it from there.
	Suppressed bool `json:"suppressed"`
}

type bounce struct {
	Type string `json:"type"`
}

// body is the webhook that tells of an event.
func body(e store.Event, m store.Message) []byte {
	d := &data{EmailID: m.ID, From: m.Sender, To: []string{m.Recipient}, CreatedAt: m.CreatedAt.UTC(),
		Code: e.Code, Reason: e.Reason, Suppressed: e.Suppressed}
	if e.Kind == store.EventBounced {
		// How the bounce is typed is about the address, and so follows the
		// rule that decides suppression; whether the address is in fact on
		// the list is said apart, since a suppression can have been lifted.
		_, atFault := store.SuppressionFor(e.Kind, e.Permanent, e.Code)
		d.Bounce = &bounce{Type: bounceTransient}
		switch {
		case e.Permanent && atFault:
			d.Bounce.Type = bouncePermanent
		case e.Permanent:
			d.Bounce.Type = bouncePolicy
		}
	}
	return encode(payload{Type: typeOf[e.Kind], CreatedAt: e.At.UTC(), Data: d})
}

// suppressedBody is the webhook that tells of a suppression on its own.
// It is about an address, and names no email.
func suppressedBody(s store.Suppression) []byte {
	return encode(map[string]any{"type": TypeSuppressed, "created_at": s.CreatedAt.UTC(),
		"data": map[string]string{"domain": s.Domain, "address": s.Address, "reason": s.Reason}})
}

// receivedBody is the webhook that tells of an email that came.
func receivedBody(in store.Inbound) []byte {
	return encode(map[string]any{"type": TypeReceived, "created_at": in.ReceivedAt.UTC(),
		"data": map[string]any{"inbound_id": in.ID, "from": in.Sender, "to": []string{in.Recipient}, "received_at": in.ReceivedAt.UTC()}})
}

// bodyOf is the webhook a delivery sends.
func bodyOf(d store.Due) []byte {
	if d.Inbound != nil {
		return receivedBody(*d.Inbound)
	}
	if d.Suppression != nil {
		return suppressedBody(*d.Suppression)
	}
	return body(d.Event, d.Message)
}

// testBody is the webhook that tries an endpoint out. It is about no email.
func testBody(at time.Time) []byte {
	return encode(map[string]any{"type": TypeTest, "created_at": at.UTC(), "data": struct{}{}})
}

func encode(p any) []byte {
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // an address is written as it is
	_ = enc.Encode(p)
	return []byte(strings.TrimSuffix(out.String(), "\n"))
}
