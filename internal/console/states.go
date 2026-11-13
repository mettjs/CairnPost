package console

import "github.com/mettjs/cairnpost/internal/store"

// said is how a state of an email is named and explained to a person.
type stateWords struct {
	// Label names it in a list; Meaning says it in a sentence; Tone is
	// how it is coloured: "ok", "failing" or "off".
	Label, Meaning, Tone string
}

// states has words for every state an email can be in.
var states = map[string]stateWords{
	store.StateSending:    {"Sending", "It was handed to the mail provider, whose answer is not known yet.", "off"},
	store.StateSent:       {"Sent", "The mail provider took it. Nothing more has been heard.", "off"},
	store.StateDeferred:   {"Delayed", "The receiving server said not now. The mail provider keeps trying.", "off"},
	store.StateDelivered:  {"Delivered", "The receiving server took it.", "ok"},
	store.StateBounced:    {"Bounced", "The receiving server refused it.", "failing"},
	store.StateComplained: {"Marked as spam", "It arrived, and whoever received it marked it as spam.", "failing"},
	store.StateFailed:     {"Failed", "The mail provider refused it or could not send it. It did not arrive.", "failing"},
	store.StateUnknown:    {"Unknown", "The mail provider's answer never came, so it is not known whether it went.", "off"},
}

// wordsOf answers a state's words; a state this console does not know is
// shown by its own name, plainly.
func wordsOf(state string) stateWords {
	if w, known := states[state]; known {
		return w
	}
	return stateWords{Label: state, Tone: "off"}
}

// happened names a kind of event. Each kind moves an email to a state,
// and is named as that state is.
func happened(kind string) string {
	if state, known := store.StateOf[kind]; known {
		return wordsOf(state).Label
	}
	return kind
}
