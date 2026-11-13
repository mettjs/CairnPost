package console

import (
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
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
