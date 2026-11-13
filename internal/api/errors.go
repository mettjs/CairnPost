package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/mettjs/cairnpost/internal/driver"
	"github.com/mettjs/cairnpost/internal/mail"
)

// problem is every error's body: the status, a name a program can switch
// on, and a sentence for a person saying what to do. ID is the email's id,
// when the send got far enough to have one: GET /emails/{id} then says
// what became of it.
type problem struct {
	StatusCode int    `json:"statusCode"`
	Name       string `json:"name"`
	Message    string `json:"message"`
	ID         string `json:"id,omitempty"`
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

// seconds is a wait as a Retry-After value: whole seconds, at least one.
func seconds(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}

// failSend answers a send that did not happen. The status is the contract:
// 422 is the request's own fault and will never succeed, 409 with
// invalid_idempotent_request means an earlier request under the key was
// sent, and everything else is worth trying again. Only upstream_unknown
// leaves it open whether an email went.
func (a *app) failSend(w http.ResponseWriter, id string, err error) {
	var (
		invalid   *mail.Invalid
		busy      *mail.Busy
		throttled *driver.Throttled
		p         problem
	)
	switch {
	case errors.As(err, &invalid):
		p = problem{http.StatusUnprocessableEntity, "validation_error", invalid.Reason, ""}
	case errors.Is(err, mail.ErrDomain):
		p = problem{http.StatusForbidden, "domain_not_allowed",
			"This API key may only send from its own domain. Use a key for this domain, or change the from address.", ""}
	case errors.Is(err, mail.ErrSuppressed):
		p = problem{http.StatusUnprocessableEntity, "recipient_suppressed",
			"This address is on the suppression list for the sending domain: an earlier email to it bounced for good " +
				"or was marked as spam, or it was added by hand. Nothing was sent now. " +
				"If that is wrong, an admin can lift it with POST /suppressions/lift.", id}
		if id != "" {
			p.Message += " An earlier try under this Idempotency-Key got no answer from the mail provider and may " +
				"have gone: the id is that try's."
		}
	case errors.Is(err, mail.ErrMismatch):
		p = problem{http.StatusConflict, "invalid_idempotent_request",
			"This Idempotency-Key was already used with a different request, which was sent. Use a new key for a new email.", ""}
	case errors.As(err, &busy):
		w.Header().Set("Retry-After", seconds(busy.After))
		p = problem{http.StatusConflict, "concurrent_idempotent_requests",
			"A send with this Idempotency-Key is in flight, or its answer has not come yet. " +
				"Try again after the time in the Retry-After header.", ""}
	case errors.Is(err, driver.ErrRejected):
		p = problem{http.StatusUnprocessableEntity, "rejected_by_upstream",
			"The mail provider refused this email for good. Check the addresses and the headers.", id}
	case errors.As(err, &throttled):
		if throttled.After > 0 {
			w.Header().Set("Retry-After", seconds(throttled.After))
		}
		p = problem{http.StatusTooManyRequests, "rate_limit_exceeded",
			"The mail provider's sending limit is reached. Nothing was sent; try again later.", id}
	case errors.Is(err, driver.ErrUnavailable):
		p = problem{http.StatusServiceUnavailable, "upstream_unavailable",
			"The mail provider is not taking mail right now. Nothing was sent; try again later.", id}
	case errors.Is(err, mail.ErrInDoubt):
		p = problem{http.StatusBadGateway, "upstream_unknown",
			"The mail provider did not answer, so the email may or may not have gone. " +
				"Try again with the same Idempotency-Key.", id}
	default:
		// A failure of our own, before the provider was asked.
		a.Log.Error("api: a send could not be started", "error", err)
		p = problem{http.StatusServiceUnavailable, "unavailable",
			"CairnPost could not record the send, so nothing was sent. Try again shortly.", ""}
	}
	write(w, p.StatusCode, p)
}

// body reads a request's JSON into v, and answers false, having said why,
// when it cannot. shape is the sentence saying what the body must be.
func (a *app) body(w http.ResponseWriter, r *http.Request, v any, shape string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, a.MaxBodyBytes))
	// A field CairnPost does not know is refused, never dropped: an email
	// sent without the cc its caller asked for is worse than no email.
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooBig):
		fail(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request body is larger than this server accepts.")
	default:
		fail(w, http.StatusUnprocessableEntity, "validation_error", shape)
	}
	return false
}
