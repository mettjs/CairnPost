package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// recipients reads "to", "cc" or "bcc" as one address or a list of
// them. null and "" are no address, as a client writes a field it left
// empty.
type recipients []string

func (r *recipients) UnmarshalJSON(raw []byte) error {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if *r = nil; one != "" {
			*r = recipients{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return errors.New("must be an address or a list of addresses")
	}
	*r = many
	return nil
}

type sendBody struct {
	From    string            `json:"from"`
	To      recipients        `json:"to"`
	Cc      recipients        `json:"cc"`
	Bcc     recipients        `json:"bcc"`
	ReplyTo string            `json:"reply_to"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html"`
	Text    string            `json:"text"`
	Headers map[string]string `json:"headers"`
}

// sendEmail is POST /emails: one email, to one address or several. It
// answers once the mail provider has taken the email, with the email's
// id, and with the addresses it was not sent to because they are
// suppressed, if any were.
func (a *app) sendEmail(w http.ResponseWriter, r *http.Request) {
	var body sendBody
	if !a.body(w, r, &body, "The body must be JSON with from, to, subject and html or text; "+
		"cc, bcc, reply_to and headers are optional. No other field is supported.") {
		return
	}
	c := callerOf(r)
	sent, err := a.Mail.Send(r.Context(), c.key, c.presented, mail.Request{
		From: body.From, To: body.To, Cc: body.Cc, Bcc: body.Bcc, ReplyTo: body.ReplyTo, Subject: body.Subject,
		HTML: body.HTML, Text: body.Text, Headers: body.Headers,
		IdemKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		a.failSend(w, sent.ID, err)
		return
	}
	write(w, http.StatusOK, sentView{sent.ID, sent.Suppressed})
}

// sentView is what a send answers.
type sentView struct {
	ID string `json:"id"`
	// Suppressed are the addresses the email was not sent to, because
	// they are on the sending domain's suppression list; left out when
	// it went to all of them.
	Suppressed []string `json:"suppressed,omitempty"`
}

type emailView struct {
	ID   string `json:"id"`
	From string `json:"from"`
	// To is the first address the email was sent to, and State what
	// became of it there. Recipients has every address and its own.
	To         string          `json:"to"`
	State      string          `json:"state"`
	Recipients []recipientView `json:"recipients"`
	// Suppressed are the addresses it was asked to go to and did not,
	// being on the suppression list when it was sent.
	Suppressed []string    `json:"suppressed,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	Events     []eventView `json:"events"`
	// Content is what the email said: there only where the operator keeps
	// it, while it is kept, and for an admin key that may see the sending
	// domain.
	Content *mail.Content `json:"content,omitempty"`
}

// recipientView is one address of an email and what became of it there.
type recipientView struct {
	Address string `json:"address"`
	// Kind is "to", "cc" or "bcc".
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// eventView is one thing that happened to an email after it was sent.
type eventView struct {
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	// To is the address it happened at.
	To string `json:"to"`
	// Permanent says, for a bounce, that the address will never take mail.
	Permanent bool `json:"permanent,omitempty"`
	// Code is the receiving server's status ("5.1.1"); Reason the mail
	// provider's short name for what happened ("permanent_failure").
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Detail is the receiving server's own sentence: there under the rule
	// for an email's content, since it can quote the email.
	Detail string `json:"detail,omitempty"`
}

// getEmail is GET /emails/{id}: what became of an email, and each thing
// that happened to it. A key sees the emails it sent; an admin key sees
// any, or its sending domain's if it is bound to one.
func (a *app) getEmail(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	m, events, err := a.Mail.MessageFor(r.Context(), c.key, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "No email has this id.")
	case err != nil:
		a.Log.Error("api: an email could not be read", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The email could not be read. Try again shortly.")
	default:
		// Times are answered in UTC, whatever zone the server runs in.
		view := emailView{ID: m.ID, From: m.Sender, To: m.Recipient, State: m.State, Recipients: []recipientView{},
			Suppressed: m.LeftOut, CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.Heard().UTC(), Events: []eventView{}}
		for _, r := range m.Recipients {
			view.Recipients = append(view.Recipients, recipientView{r.Address, r.Kind, r.State, r.UpdatedAt.UTC()})
		}
		// What it said is for whoever runs CairnPost: an admin key. A
		// send key is never shown it, not even for its own emails. If it
		// cannot be read, the record is answered without it.
		var said map[string]string
		if mail.ReadsContent(c.key) {
			if view.Content, err = a.Mail.Content(r.Context(), m.ID); err != nil {
				a.Log.Error("api: an email's kept content could not be read", "error", err)
			}
			if len(events) > 0 {
				if said, err = a.Mail.Details(r.Context(), m.ID); err != nil {
					a.Log.Error("api: what was said of an email could not be read", "error", err)
				}
			}
		}
		for _, e := range events {
			view.Events = append(view.Events, eventView{e.Kind, e.At.UTC(), e.Recipient, e.Permanent, e.Code, e.Reason, said[e.ID]})
		}
		write(w, http.StatusOK, view)
	}
}

// CapturedEmail is an email a development driver kept instead of sending.
type CapturedEmail struct {
	ID       string            `json:"id"`
	At       time.Time         `json:"at"`
	From     string            `json:"from"`
	FromName string            `json:"from_name,omitempty"`
	To       []string          `json:"to"`
	Cc       []string          `json:"cc,omitempty"`
	Bcc      []string          `json:"bcc,omitempty"`
	ReplyTo  string            `json:"reply_to,omitempty"`
	Subject  string            `json:"subject"`
	HTML     string            `json:"html,omitempty"`
	Text     string            `json:"text,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// captured is GET /capture/emails: the emails kept instead of sent, newest
// first. Mounted only when such a driver is in use, for an admin key only.
func (a *app) captured(w http.ResponseWriter, _ *http.Request) {
	out := a.Captured()
	if out == nil {
		out = []CapturedEmail{}
	}
	write(w, http.StatusOK, map[string]any{"data": out})
}
