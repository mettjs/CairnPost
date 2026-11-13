package console

import "net/http"

// said is the cookie a sentence for the next page travels in: one word
// the console knows, read once and then forgotten. Nothing anybody typed
// is in it, and nothing in a URL can make a page claim something happened.
const said = "cairnpost_console_said"

// notices are the sentences a request can ask the next page to say.
var notices = map[string]string{
	"revoked": "The key was revoked. Anything still using it is refused from now on.",
	"revoked-own": "You revoked the key you were signed in with, so you are signed out. " +
		"Sign in with another admin key.",
	"ended": "Your session ended. Sign in again.",
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
	return notices[held.Value]
}
