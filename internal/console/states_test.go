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
