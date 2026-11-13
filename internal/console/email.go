package console

import (
	"errors"
	"net/http"
	"slices"
	"strings"

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
	v := emailView{Email: m}
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
	c.render(w, r, http.StatusOK, "email", v)
}
