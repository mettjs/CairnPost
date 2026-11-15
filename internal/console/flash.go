package console

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/mettjs/cairnpost/internal/webhook"
)

// said is the cookie a sentence for the next page travels in: one word
// the console knows, or such a word and a count, read once and then
// forgotten. Nothing anybody typed
// is in it, and nothing in a URL can make a page claim something happened.
const said = "cairnpost_console_said"

// notices are the sentences a request can ask the next page to say.
var notices = map[string]string{
	"revoked": "The key was revoked. Anything still using it is refused from now on.",
	"revoked-own": "You revoked the key you were signed in with, so you are signed out. " +
		"Sign in with another admin key.",
	"email-cancelled":     "The email was cancelled. It will not be sent, and what it said is deleted.",
	"email-not-waiting":   "That email was no longer waiting: it was sent, is being sent, or was cancelled already. Nothing changed.",
	"ended":               "Your session ended. Sign in again.",
	"suppressed":          "The address is suppressed. Nothing is sent to it from that domain from now on.",
	"suppressed-already":  "That address was suppressed for that domain already. Nothing changed.",
	"lifted":              "The suppression was lifted. Emails go to that address again.",
	"lifted-already":      "That address was no longer suppressed. Nothing changed.",
	"template-made":       "The template was made. A send can name it now.",
	"template-changed":    "The template was changed. Emails asked for from now on are made from it.",
	"template-deleted":    "The template was deleted. A send that names it is refused.",
	"endpoint-changed":    "The endpoint was changed.",
	"endpoint-on":         "The endpoint is on. What it was owed when it was turned off is sent now; what happened while it was off is not.",
	"endpoint-off":        "The endpoint is off. What it is already owed waits. It is told nothing of what happens while it is off, even later.",
	"retry-off":           "The endpoint is off, so the webhook is tried when it is turned on.",
	"retry-paused":        "The endpoint is paused, so the webhook is tried when it is resumed.",
	"endpoint-resumed":    "The endpoint is resumed. What it was owed while it was paused is sent now, with what was given up while it failed.",
	"endpoint-not-paused": "The endpoint was not paused. Nothing changed.",
	"endpoint-on-paused":  "The endpoint is on, and still paused for failing. Resume it to have what it is owed sent.",
	"endpoint-removed":    "The endpoint was removed, with everything it was still owed.",
	"retried":             "The webhook is being tried again now.",
	"retry-gone":          "That webhook was delivered meanwhile, or is no longer on record. Nothing changed.",
	"unkept": "Your browser did not keep the sign-in. The console needs https: over plain http, " +
		"which is right only on your own machine, start the server with CAIRNPOST_CONSOLE_PLAIN_HTTP=on.",
}

// flash asks the next page to say a sentence. It is not marked Secure, so
// that it can say a Secure cookie was dropped; it holds no secret.
func (c *console) flash(w http.ResponseWriter, word string) {
	c.keep(w, said, word, 60, false)
}

// heard answers the sentence asked for by the request before, and
// forgets it.
func (c *console) heard(w http.ResponseWriter, r *http.Request) string {
	held, err := r.Cookie(said)
	if err != nil {
		return ""
	}
	c.drop(w, said, false)
	return flashed(held.Value)
}

// flashed is what a flash says. Most are one word the console knows. Two
// carry what cannot be a fixed sentence, each checked for what it is: a
// count, and what a test of an endpoint met (a status and one of the
// webhook service's own names for a failure). Anything else says nothing.
func flashed(flash string) string {
	parts := strings.Split(flash, ":")
	switch {
	case len(parts) == 1:
		return notices[flash]
	case parts[0] == "lifted-all" && len(parts) == 2:
		n, err := strconv.Atoi(parts[1])
		switch {
		case err != nil || n < 0 || len(parts[1]) > 9:
			return ""
		case n == 0:
			return "Nothing was lifted: no address was suppressed for that domain and reason."
		case n == 1:
			return "1 address can be sent to again."
		}
		return strconv.Itoa(n) + " addresses can be sent to again."
	case parts[0] == "tested" && len(parts) == 3:
		status, err := strconv.Atoi(parts[1])
		if err != nil || status < 0 || status > 999 || (parts[2] != "" && !slices.Contains(webhook.Failures, parts[2])) {
			return ""
		}
		if parts[2] == "" {
			return "The endpoint took the test webhook. " + met(status, "")
		}
		return "The test webhook was not taken. " + met(status, parts[2])
	}
	return ""
}
