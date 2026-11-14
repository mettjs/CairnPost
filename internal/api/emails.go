package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	// ScheduledAt is when to send it, as RFC 3339 says a time; left
	// out, it is sent now.
	ScheduledAt string `json:"scheduled_at"`
	// Attachments are files, each with its bytes in base64.
	Attachments []struct {
		Filename    string `json:"filename"`
		Content     string `json:"content"`
		ContentType string `json:"content_type"`
		ContentID   string `json:"content_id"`
	} `json:"attachments"`
}

// request is the body as the send service takes it: an *Invalid for a
// file that is not base64. The body lets go of each file as it is read,
// so none is held a second time while it is sent.
func (b *sendBody) request() (mail.Request, error) {
	var files []mail.Attachment
	for i, f := range b.Attachments {
		raw, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return mail.Request{}, &mail.Invalid{Field: "attachments",
				Reason: fmt.Sprintf(`Attachment %d: "content" must be the file's bytes in base64.`, i+1)}
		}
		files = append(files, mail.Attachment{Filename: f.Filename, Type: f.ContentType, ContentID: f.ContentID, Content: raw})
		b.Attachments[i].Content = ""
	}
	at, err := when(b.ScheduledAt)
	if err != nil {
		return mail.Request{}, err
	}
	return mail.Request{From: b.From, To: b.To, Cc: b.Cc, Bcc: b.Bcc, ReplyTo: b.ReplyTo, Subject: b.Subject,
		HTML: b.HTML, Text: b.Text, Headers: b.Headers, Attachments: files, ScheduledAt: at}, nil
}

// when reads "scheduled_at": the zero time for none, an *Invalid for
// what is no time.
func when(typed string) (time.Time, error) {
	if typed == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339, typed)
	if err != nil {
		return time.Time{}, &mail.Invalid{Field: "scheduled_at",
			Reason: `"scheduled_at" must be a time with its zone, such as "2030-01-31T09:00:00Z".`}
	}
	return at, nil
}

// sendEmail is POST /emails: one email, to one address or several. It
// answers once the mail provider has taken the email, with the email's
// id, and with the addresses it was not sent to because they are
// suppressed, if any were.
func (a *app) sendEmail(w http.ResponseWriter, r *http.Request) {
	// A send that carries files is held in memory while it is sent:
	// only so much of them is taken at one time.
	release, ok := a.uploads.take(r.ContentLength, a.MaxBodyBytes, max(a.MaxEmailBytes, a.MaxBodyBytes), a.MaxUploadBytes)
	if !ok {
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "unavailable", "The server is taking as many large emails as it can hold. Nothing was sent. Try again shortly.")
		return
	}
	defer release()
	var body sendBody
	if !a.bodyUpTo(w, r, &body, "The body must be JSON with from, to, subject and html or text; "+
		"cc, bcc, reply_to, headers, attachments and scheduled_at are optional. No other field is supported.", max(a.MaxEmailBytes, a.MaxBodyBytes)) {
		return
	}
	req, err := body.request()
	if err != nil {
		a.failSend(w, "", err)
		return
	}
	req.IdemKey = r.Header.Get("Idempotency-Key")
	c := callerOf(r)
	sent, err := a.Mail.Send(r.Context(), c.key, c.presented, req)
	if err != nil {
		a.failSend(w, sent.ID, err)
		return
	}
	write(w, http.StatusOK, sentOf(sent))
}

// sentView is what a send answers.
type sentView struct {
	ID string `json:"id"`
	// Suppressed are the addresses the email was not sent to, because
	// they are on the sending domain's suppression list; left out when
	// it went to all of them.
	Suppressed []string `json:"suppressed,omitempty"`
	// ScheduledAt is when it will be sent, for an email that was asked
	// for ahead of time and waits.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

func sentOf(r mail.Receipt) sentView {
	v := sentView{ID: r.ID, Suppressed: r.Suppressed}
	if !r.ScheduledAt.IsZero() {
		at := r.ScheduledAt.UTC()
		v.ScheduledAt = &at
	}
	return v
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
	Suppressed []string `json:"suppressed,omitempty"`
	// ScheduledAt is when an email that waits will be sent, or would
	// have been, had it not been cancelled. Reason says why it was
	// cancelled when nobody asked: "suppressed" or "key_revoked".
	ScheduledAt *time.Time  `json:"scheduled_at,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Events      []eventView `json:"events"`
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
			Suppressed: m.LeftOut, CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.Heard().UTC(), Events: []eventView{}, Reason: m.Reason}
		if !m.SendAt.IsZero() {
			at := m.SendAt.UTC()
			view.ScheduledAt = &at
		}
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
	// Attachments say what each file was; its bytes are not held.
	Attachments []mail.File `json:"attachments,omitempty"`
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
