package console

import (
	"strconv"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// failures says, for each reason a webhook can fail for, what happened
// and what to look at.
var failures = map[string]string{
	webhook.FailTimeout: "It did not answer in time.",
	webhook.FailRefused: "Nothing is listening at that address.",
	webhook.FailDNS:     "Its host name leads nowhere.",
	webhook.FailTLS:     "Its certificate does not check out.",
	webhook.FailBlocked: "Its address is not on the public internet. An endpoint on a private network needs CAIRNPOST_WEBHOOK_ALLOW_PRIVATE on this server.",
	webhook.FailStatus:  "It answered with a status that is not a success.",
	webhook.FailOther:   "The request failed before any answer came.",
}

// met says what a try at an endpoint met: the status it answered with, 0
// for none, and why it failed, "" when it did not.
func met(status int, failure string) string {
	switch {
	case failure == "" && status != 0:
		return "It answered " + strconv.Itoa(status) + "."
	case failure == "":
		return ""
	case failure == webhook.FailStatus && status != 0:
		return "It answered " + strconv.Itoa(status) + ", which is not a success."
	}
	if words, known := failures[failure]; known {
		return words
	}
	return failures[webhook.FailOther]
}

// deliveryStates names each state a delivery can be in, and how it is
// coloured.
var deliveryStates = map[string][2]string{
	store.DeliveryPending:   {"Waiting", "off"},
	store.DeliveryDelivered: {"Delivered", "ok"},
	store.DeliveryFailed:    {"Given up", "failing"},
}

// deliveryWords answers a delivery state's name and colour; one this
// console does not know is shown by its own name.
func deliveryWords(state string) (label, tone string) {
	if w, known := deliveryStates[state]; known {
		return w[0], w[1]
	}
	return state, "off"
}

// endpointStates names each state an endpoint can be in, and how it is
// coloured.
var endpointStates = map[string][2]string{
	webhook.StateOn:     {"On", "ok"},
	webhook.StateOff:    {"Off", "off"},
	webhook.StatePaused: {"Paused", "failing"},
}

// hookFields names, as the forms do, the parts of an endpoint the
// webhook service says what they must be.
var hookFields = map[string]string{"url": "The URL", "description": "The description"}

// hookProblems are the forms' own words for the parts whose rule reads
// differently in a form than in the API.
var hookProblems = map[string]string{
	"events": "Tick at least one event, or choose every event.",
	"domain": "The sending domain must be a name such as mail.example.com, or empty for every domain.",
}

// hookProblem says why an endpoint was refused, in the form's words:
// its own, or the part as the form names it and what the service says
// it must be. Anything else is said as the service says it.
func hookProblem(invalid *webhook.Invalid) string {
	if words, known := hookProblems[invalid.Field]; known {
		return words
	}
	if name, known := hookFields[invalid.Field]; known && invalid.Must != "" {
		return name + " " + invalid.Must
	}
	return invalid.Reason
}
