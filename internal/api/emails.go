package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// recipients reads "to" as one address or a list of them.
type recipients []string

func (r *recipients) UnmarshalJSON(raw []byte) error {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		*r = recipients{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return errors.New("to must be an address or a list of addresses")
	}
	*r = many
	return nil
}

type sendBody struct {
	From    string            `json:"from"`
	To      recipients        `json:"to"`
	ReplyTo string            `json:"reply_to"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html"`
	Text    string            `json:"text"`
	Headers map[string]string `json:"headers"`
}

// sendEmail is POST /emails: one email to one recipient. It answers once
// the mail provider has taken the email, with the email's id.
func (a *app) sendEmail(w http.ResponseWriter, r *http.Request) {
	var body sendBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, a.MaxBodyBytes))
	// A field CairnPost does not know is refused, never dropped: an email
	// sent without the cc its caller asked for is worse than no email.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request body is larger than this server accepts.")
			return
		}
		fail(w, http.StatusUnprocessableEntity, "validation_error",
			"The body must be JSON with from, to, subject and html or text; reply_to and headers are optional. "+
				"No other field is supported.")
		return
	}
	c := callerOf(r)
	id, err := a.Mail.Send(r.Context(), c.key, c.presented, mail.Request{
		From: body.From, To: body.To, ReplyTo: body.ReplyTo, Subject: body.Subject,
		HTML: body.HTML, Text: body.Text, Headers: body.Headers,
		IdemKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		a.failSend(w, err)
		return
	}
	write(w, http.StatusOK, map[string]string{"id": id})
}

type emailView struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// getEmail is GET /emails/{id}: what became of an email. A key sees the
// emails it sent; an admin key sees any.
func (a *app) getEmail(w http.ResponseWriter, r *http.Request) {
	m, err := a.Mail.Message(r.Context(), r.PathValue("id"))
	c := callerOf(r)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && m.KeyID != c.key.ID && c.key.Scope != store.ScopeAdmin:
		fail(w, http.StatusNotFound, "not_found", "No email has this id.")
	case err != nil:
		a.Log.Error("api: an email could not be read", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The email could not be read. Try again shortly.")
	default:
		write(w, http.StatusOK, emailView{m.ID, m.Sender, m.Recipient, m.State, m.CreatedAt, m.UpdatedAt})
	}
}

type capturedView struct {
	ID      string            `json:"id"`
	At      time.Time         `json:"at"`
	From    string            `json:"from"`
	To      string            `json:"to"`
	ReplyTo string            `json:"reply_to,omitempty"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html,omitempty"`
	Text    string            `json:"text,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// captured is GET /capture/emails: what the capture driver holds, newest
// first. Mounted only when capture is the driver, for an admin key only.
func (a *app) captured(w http.ResponseWriter, _ *http.Request) {
	out := []capturedView{}
	for _, c := range a.Capture.Held() {
		out = append(out, capturedView{c.ID, c.At, c.From, c.To, c.ReplyTo, c.Subject, c.HTML, c.Text, c.Headers})
	}
	write(w, http.StatusOK, map[string]any{"data": out})
}
