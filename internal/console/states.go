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

// waitingStates has words for the states of an email that was asked for
// ahead of time and is not among the sent ones.
var waitingStates = map[string]stateWords{
	store.StateScheduled: {"Scheduled", "It waits to be sent at the time it was asked for.", "off"},
	store.StateCancelled: {"Cancelled", "It was to be sent later and was cancelled first. It was not sent.", "off"},
}

// cancelledFor says why an email that waited was cancelled when nobody
// asked for that.
var cancelledFor = map[string]string{
	store.HeldSuppressed: "When its time came, every address it named in the open was on the suppression list.",
	store.HeldKeyRevoked: "The API key that asked for it was revoked before its time came.",
	store.HeldFailed:     "This server could not send it, each time it tried. The server's log says why.",
}

// wordsOf answers a state's words; a state this console does not know is
// shown by its own name, plainly.
func wordsOf(state string) stateWords {
	if w, known := states[state]; known {
		return w
	}
	if w, known := waitingStates[state]; known {
		return w
	}
	return stateWords{Label: state, Tone: "off"}
}

// reasons has words for every reason an address is suppressed for: as a
// label, and as the end of "because ...".
var reasons = map[string][2]string{
	store.ReasonBounce:    {"Bounced for good", "an email to it bounced for good"},
	store.ReasonComplaint: {"Marked as spam", "its owner marked an email as spam"},
	store.ReasonManual:    {"Added by hand", "someone added it by hand"},
}

// why names a reason; one this console does not know by its own name.
func why(reason string) string {
	if w, known := reasons[reason]; known {
		return w[0]
	}
	return reason
}

// because ends the sentence "suppressed because ..." for a reason; one
// this console does not know is named plainly.
func because(reason string) string {
	if w, known := reasons[reason]; known {
		return w[1]
	}
	return "of " + reason
}

// happened names a kind of event. Each kind moves an email to a state,
// and is named as that state is.
func happened(kind string) string {
	if state, known := store.StateOf[kind]; known {
		return wordsOf(state).Label
	}
	return kind
}

// outcome is how many addresses of an email are in a state.
type outcome struct {
	State string
	N     int
}

// outcomes are the states an email's addresses are in, in the order of
// an email's life, each with how many are in it: what became of an
// email that went to several.
func outcomes(m store.Message) []outcome {
	in := map[string]int{}
	for _, r := range m.Recipients {
		in[r.State]++
	}
	var out []outcome
	for _, state := range store.States {
		if in[state] > 0 {
			out = append(out, outcome{state, in[state]})
		}
	}
	return out
}

// kindWords names each kind of recipient.
var kindWords = map[string]string{store.KindTo: "To", store.KindCc: "Copy", store.KindBcc: "Hidden copy"}
