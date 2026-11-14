package api

import (
	"errors"
	"net/http"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// cancelEmail is POST /emails/{id}/cancel: an email that waits to be
// sent will not be, and what it said is deleted. The key that asked for
// the email may, and an admin key that may read it.
func (a *app) cancelEmail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Mail.Cancel(r.Context(), callerOf(r).key, id); err != nil {
		a.failWaiting(w, err)
		return
	}
	write(w, http.StatusOK, map[string]string{"id": id, "state": store.StateCancelled})
}

// rescheduleEmail is PATCH /emails/{id}: an email that waits is given
// another time.
func (a *app) rescheduleEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ScheduledAt string `json:"scheduled_at"`
	}
	if !a.body(w, r, &body, `The body must be JSON with scheduled_at, the time to send the email at. No other field can be changed.`) {
		return
	}
	at, err := when(body.ScheduledAt)
	if err == nil && at.IsZero() {
		err = &mail.Invalid{Field: "scheduled_at", Reason: `Give "scheduled_at", the time to send the email at.`}
	}
	id := r.PathValue("id")
	if err == nil {
		err = a.Mail.Reschedule(r.Context(), callerOf(r).key, id, at)
	}
	if err != nil {
		a.failWaiting(w, err)
		return
	}
	at = at.UTC()
	write(w, http.StatusOK, sentView{ID: id, ScheduledAt: &at})
}

// failWaiting answers a change to a waiting email that was not made.
func (a *app) failWaiting(w http.ResponseWriter, err error) {
	var invalid *mail.Invalid
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "No email has this id.")
	case errors.As(err, &invalid), errors.Is(err, mail.ErrNotScheduled):
		a.failSend(w, "", err)
	default:
		a.Log.Error("api: a waiting email could not be changed", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The email could not be changed. Try again shortly.")
	}
}
