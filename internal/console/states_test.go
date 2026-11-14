package console

import (
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// Every state an email can be in, and every kind of event, has words of
// its own: none is shown by its raw name.
func TestEveryStateHasWords(t *testing.T) {
	if len(states) != len(store.States) {
		t.Fatalf("%d states have words, of %d", len(states), len(store.States))
	}
	for _, state := range store.States {
		w, known := states[state]
		if !known || w.Label == "" || w.Meaning == "" || (w.Tone != "ok" && w.Tone != "failing" && w.Tone != "off") {
			t.Errorf("%s: %+v", state, w)
		}
	}
	for kind := range store.StateOf {
		if happened(kind) == "" || states[store.StateOf[kind]].Label != happened(kind) {
			t.Errorf("an event of kind %s is named %q", kind, happened(kind))
		}
	}
	for _, state := range []string{store.StateScheduled, store.StateCancelled} {
		if w := wordsOf(state); w.Label == state || w.Meaning == "" {
			t.Errorf("%s: %+v", state, w)
		}
	}
	for _, reason := range []string{store.HeldSuppressed, store.HeldKeyRevoked} {
		if cancelledFor[reason] == "" {
			t.Errorf("an email cancelled for %s has no words", reason)
		}
	}
	if w := wordsOf("a state from later"); w.Label != "a state from later" || w.Tone != "off" {
		t.Errorf("a state this console does not know: %+v", w)
	}
}

// Every reason an address is suppressed for has words of its own.
func TestEveryReasonHasWords(t *testing.T) {
	if len(reasons) != len(store.Reasons) {
		t.Fatalf("%d reasons have words, of %d", len(reasons), len(store.Reasons))
	}
	for _, reason := range store.Reasons {
		if w := reasons[reason]; w[0] == "" || w[1] == "" || why(reason) != w[0] || because(reason) != w[1] {
			t.Errorf("%s: %q", reason, w)
		}
	}
	if why("later") != "later" || because("later") != "of later" {
		t.Errorf("a reason this console does not know: %q, %q", why("later"), because("later"))
	}
}

// Every way a webhook can fail, and every state a delivery can be in,
// has words of its own.
func TestEveryWebhookOutcomeHasWords(t *testing.T) {
	if len(failures) != len(webhook.Failures) || len(deliveryStates) != len(store.DeliveryStates) {
		t.Fatalf("%d failures and %d states have words, of %d and %d", len(failures), len(deliveryStates),
			len(webhook.Failures), len(store.DeliveryStates))
	}
	for _, failure := range webhook.Failures {
		if failures[failure] == "" || met(0, failure) == "" || flashed("tested:0:"+failure) == "" {
			t.Errorf("%s has no words", failure)
		}
	}
	// And every state an endpoint can be in.
	if len(endpointStates) != len(webhook.States) {
		t.Fatalf("%d endpoint states have words, of %d", len(endpointStates), len(webhook.States))
	}
	for _, state := range webhook.States {
		if w := endpointStates[state]; w[0] == "" || (w[1] != "ok" && w[1] != "failing" && w[1] != "off") {
			t.Errorf("an endpoint that is %s: %q", state, w)
		}
	}
	for _, state := range store.DeliveryStates {
		if label, tone := deliveryWords(state); label == "" || label == state || (tone != "ok" && tone != "failing" && tone != "off") {
			t.Errorf("%s: %q %q", state, label, tone)
		}
	}
	for flash, want := range map[string]string{
		"tested:204:":       "The endpoint took the test webhook. It answered 204.",
		"tested:500:status": "The test webhook was not taken. It answered 500, which is not a success.",
		"tested:0:blocked":  "The test webhook was not taken. Its address is not on the public internet. An endpoint on a private network needs CAIRNPOST_WEBHOOK_ALLOW_PRIVATE on this server.",
		"lifted-all:2":      "2 addresses can be sent to again.",
	} {
		if got := flashed(flash); got != want {
			t.Errorf("%s says %q", flash, got)
		}
	}
	// A flash carries a number or one of the service's names, never text.
	for _, forged := range []string{"tested:200:<b>", "tested:abc:", "tested:200", "tested:200:status:more", "tested:-1:", "tested:1000:",
		"lifted-all:x", "revoked:1", "nonsense", ":"} {
		if got := flashed(forged); got != "" {
			t.Errorf("%q says %q", forged, got)
		}
	}
}
