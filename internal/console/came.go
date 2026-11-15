package console

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/mettjs/cairnpost/internal/download"
	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/store"
)

// cameView is one email that came: who from and for, and how it reads.
type cameView struct {
	Email store.Inbound
	Says  inbound.Read
}

// receivedShown is the part of the emails page with the emails that
// came and are kept, newest first. It reads no email that was sent.
func (c *console) receivedShown(w http.ResponseWriter, r *http.Request) {
	v := emailsView{Tab: "received", For: handleOf(r), Takes: c.Inbound.On(), KeptFor: span(c.Inbound.KeptFor())}
	before := r.URL.Query().Get("before")
	list, next, err := c.Inbound.List(r.Context(), who(r), before, shownEmails)
	if err != nil {
		c.Log.Error("console: the emails that came could not be read", "error", err)
		v.Unread = true
	}
	if v.Came, v.Paged, v.Newest = list, before != "", at("/emails", url.Values{"tab": {"received"}}); next != "" {
		v.Older = at("/emails", url.Values{"tab": {"received"}, "before": {next}})
	}
	v.Tabs = c.emailTabs(r, v.Tab, v.For)
	c.render(w, r, http.StatusOK, "emails", v)
}

// came answers what a request about an email that came met, if it met
// something: an email the signed-in key may not read is, to it, not
// there. It says whether the request is answered.
func (c *console) cameFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such email. One that came is kept for a time, and deleted then.")
	default:
		c.Log.Error("console: an email that came could not be read", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The email could not be read. Try again shortly.")
	}
	return true
}

// camePage shows an email that came, as it reads: its text as text and
// its HTML as source, never drawn.
func (c *console) camePage(w http.ResponseWriter, r *http.Request) {
	in, says, err := c.Inbound.Read(r.Context(), who(r), r.PathValue("id"))
	if !c.cameFailed(w, r, err) {
		c.render(w, r, http.StatusOK, "inbound", cameView{Email: in, Says: says})
	}
}

// cameFile hands over one of its files, as a file to save. Its name is
// not logged.
func (c *console) cameFile(w http.ResponseWriter, r *http.Request) {
	part, content, err := c.Inbound.FileOf(r.Context(), who(r), r.PathValue("id"), download.Place(r.PathValue("place")))
	if !c.cameFailed(w, r, err) {
		download.Serve(w, part.Filename, content)
	}
}

// cameRaw hands over the email as it came, as a file to save.
func (c *console) cameRaw(w http.ResponseWriter, r *http.Request) {
	in, raw, err := c.Inbound.Raw(r.Context(), who(r), r.PathValue("id"))
	if !c.cameFailed(w, r, err) {
		download.Serve(w, in.ID+".eml", raw)
	}
}

// forgetPage asks before an email that came is deleted.
func (c *console) forgetPage(w http.ResponseWriter, r *http.Request) {
	in, err := c.Inbound.Get(r.Context(), who(r), r.PathValue("id"))
	if !c.cameFailed(w, r, err) {
		c.render(w, r, http.StatusOK, "forget", in)
	}
}

// forgetCame deletes an email that came, before its time.
func (c *console) forgetCame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if c.cameFailed(w, r, c.Inbound.Delete(r.Context(), who(r), id)) {
		return
	}
	c.Log.Info("console: an email that came was deleted", "by", who(r).Prefix, "inbound", id)
	c.flash(w, "inbound-deleted")
	http.Redirect(w, r, at("/emails", url.Values{"tab": {"received"}}), http.StatusSeeOther)
}
