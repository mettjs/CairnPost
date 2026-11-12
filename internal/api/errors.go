package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
)

// problem is every error's body: the status, a name a program can switch
// on, and a sentence for a person saying what to do.
type problem struct {
	StatusCode int    `json:"statusCode"`
	Name       string `json:"name"`
	Message    string `json:"message"`
}

func write(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	// Addresses are written as they are, "Name <a@b>", not as \u003c escapes.
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

func fail(w http.ResponseWriter, code int, name, message string) {
	write(w, code, problem{StatusCode: code, Name: name, Message: message})
}

// failSend answers a send that did not happen. The status is the contract:
// 422 is the request's own fault and will never succeed, 409 with
// invalid_idempotent_request means an earlier request under the key was
// sent, and everything else is worth trying again.
func (a *app) failSend(w http.ResponseWriter, err error) {
	var invalid *mail.Invalid
	var throttled *driver.Throttled
	switch {
	case errors.As(err, &invalid):
		fail(w, http.StatusUnprocessableEntity, "validation_error", "The "+invalid.Field+" "+invalid.Reason+".")
	case errors.Is(err, mail.ErrDomain):
		fail(w, http.StatusForbidden, "domain_not_allowed",
			"This API key may only send from its own domain. Use a key for this domain, or change the from address.")
	case errors.Is(err, mail.ErrMismatch):
		fail(w, http.StatusConflict, "invalid_idempotent_request",
			"This Idempotency-Key was already used with a different request, which was sent. Use a new key for a new email.")
	case errors.Is(err, mail.ErrInFlight):
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusConflict, "concurrent_idempotent_requests",
			"A send with this Idempotency-Key is still in flight. Try again in a few seconds.")
	case errors.Is(err, driver.ErrRejected):
		fail(w, http.StatusUnprocessableEntity, "rejected_by_upstream",
			"The mail provider refused this email for good. Check the addresses and the headers.")
	case errors.As(err, &throttled):
		if throttled.After > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(throttled.After.Seconds()))))
		}
		fail(w, http.StatusTooManyRequests, "rate_limit_exceeded",
			"The mail provider's sending limit is reached. Nothing was sent; try again later.")
	case errors.Is(err, driver.ErrUnavailable):
		fail(w, http.StatusServiceUnavailable, "upstream_unavailable",
			"The mail provider is not taking mail right now. Nothing was sent; try again later.")
	default:
		a.Log.Error("api: a send failed", "error", err)
		fail(w, http.StatusBadGateway, "upstream_unknown",
			"The mail provider did not answer, so the email may or may not have gone. "+
				"Try again with the same Idempotency-Key.")
	}
}
