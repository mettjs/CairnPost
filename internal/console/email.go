package console

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/mettjs/cairnpost/internal/download"
	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

// emailView is one email: its record, what happened to it, and what it
// said where the server keeps that.
type emailView struct {
	Email  store.Message
	Events []eventRow
	// Content is what it said, nil when that is not kept; Unread says it
	// may be kept but could not be read.
	Content *mail.Content
	Headers []string
	Unread  bool
	// SaidUnread says what receiving servers said may be kept but could
	// not be read.
	SaidUnread bool
	// Waits says it is an email asked for ahead of time that still
	// waits to be sent; Why, for one cancelled by nobody's asking, why.
	Waits bool
	Why   string
	// For is the handle of the address the list it was opened from was
	// narrowed to, "" for none: the way back leads to that list.
	For string
}

// eventRow is one thing that happened, and what the receiving server
// said of it where that is kept.
type eventRow struct {
	store.Event
	Said string
}

// emailPage shows one email. One the signed-in key may not read is not
// told apart from one that does not exist.
func (c *console) emailPage(w http.ResponseWriter, r *http.Request) {
	m, events, err := c.Mail.MessageFor(r.Context(), who(r), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		c.fail(w, r, http.StatusNotFound, "There is no such email. Its id may be wrong, or its record older than this server keeps.")
		return
	}
	if err != nil {
		c.Log.Error("console: an email could not be read", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The email could not be read. Try again shortly.")
		return
	}
	v := emailView{Email: m, Waits: m.State == store.StateScheduled, Why: cancelledFor[m.Reason]}
	// What it said is never logged, and neither is why it was not read
	// beyond the store's own error.
	if v.Content, err = c.Mail.Content(r.Context(), m.ID); err != nil {
		c.Log.Error("console: an email's kept content could not be read", "error", err)
		v.Unread = true
	}
	var said map[string]string
	if len(events) > 0 {
		if said, err = c.Mail.Details(r.Context(), m.ID); err != nil {
			c.Log.Error("console: what was said of an email could not be read", "error", err)
			v.SaidUnread = true
		}
	}
	for _, e := range events {
		v.Events = append(v.Events, eventRow{e, said[e.ID]})
	}
	if v.Content != nil {
		for name, value := range v.Content.Headers {
			v.Headers = append(v.Headers, name+": "+value)
		}
		slices.SortFunc(v.Headers, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	}
	v.For = handleOf(r)
	c.render(w, r, http.StatusOK, "email", v)
}

// cancelPage asks before an email that waits is cancelled: it cannot be
// undone.
func (c *console) cancelPage(w http.ResponseWriter, r *http.Request) {
	m, _, err := c.Mail.MessageFor(r.Context(), who(r), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such email. Its id may be wrong, or its record older than this server keeps.")
	case err != nil:
		c.Log.Error("console: an email could not be read", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The email could not be read. Try again shortly.")
	case m.State != store.StateScheduled:
		c.flash(w, "email-not-waiting")
		http.Redirect(w, r, at("/emails/"+url.PathEscape(m.ID), nil), http.StatusSeeOther)
	default:
		c.render(w, r, http.StatusOK, "cancel", m)
	}
}

// cancelEmail cancels an email that waits to be sent.
func (c *console) cancelEmail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := c.Mail.Cancel(r.Context(), who(r), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such email. Its id may be wrong, or its record older than this server keeps.")
		return
	case errors.Is(err, mail.ErrNotScheduled):
		c.flash(w, "email-not-waiting")
	case err != nil:
		c.Log.Error("console: an email could not be cancelled", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The email could not be cancelled. Try again shortly.")
		return
	default:
		c.Log.Info("console: a waiting email was cancelled", "by", who(r).Prefix, "message", id)
		c.flash(w, "email-cancelled")
	}
	http.Redirect(w, r, at("/emails/"+url.PathEscape(id), nil), http.StatusSeeOther)
}

// emailFile hands over what one of an email's files held, as a file to
// save: while the email waits to be sent, or where the server keeps
// what emails say and their files. Its name is not logged.
func (c *console) emailFile(w http.ResponseWriter, r *http.Request) {
	f, err := c.Mail.FileFor(r.Context(), who(r), r.PathValue("id"), download.Place(r.PathValue("place")))
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such file. What it held may no longer be kept.")
	case err != nil:
		c.Log.Error("console: an email's file could not be read", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The file could not be read. Try again shortly.")
	default:
		download.Serve(w, f.Filename, f.Content)
	}
}
