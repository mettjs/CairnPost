package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/mettjs/cairnpost/internal/mail"
)

// batchItem is what became of one email of a batch: the email's id, or
// why it was not sent. An error can carry an id too, as a send's does.
type batchItem struct {
	ID         string   `json:"id,omitempty"`
	Suppressed []string `json:"suppressed,omitempty"`
	Error      *problem `json:"error,omitempty"`
}

// sendBatch is POST /emails/batch: several emails in one request, each
// an email as POST /emails takes it. All are checked first, and one that
// could never be sent refuses the batch with nothing sent. Otherwise it
// answers what became of each, in their order: 200 when the mail
// provider took every one, 207 when it did not take some.
func (a *app) sendBatch(w http.ResponseWriter, r *http.Request) {
	most := max(a.MaxEmailBytes, a.MaxBodyBytes)
	release, ok := a.uploads.take(r.ContentLength, a.MaxBodyBytes, most, a.MaxUploadBytes)
	if !ok {
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "unavailable", "The server is taking as many large emails as it can hold. Nothing was sent. Try again shortly.")
		return
	}
	defer release()
	var bodies []sendBody
	if !a.bodyUpTo(w, r, &bodies, "The body must be a JSON list of emails, each with from, to, subject and html or text; "+
		"cc, bcc, reply_to, headers and attachments are optional. No other field is supported.", most) {
		return
	}
	reqs := make([]mail.Request, len(bodies))
	for i := range bodies {
		req, err := bodies[i].request()
		if err != nil {
			a.failBatch(w, &mail.BatchInvalid{Index: i, Err: err})
			return
		}
		reqs[i] = req
	}
	if a.BatchTime > 0 {
		// A batch answers later than anything else does: the time it
		// may take is its own, counted from here.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(a.BatchTime))
	}
	c := callerOf(r)
	done, err := a.Mail.SendBatch(r.Context(), c.key, c.presented, r.Header.Get("Idempotency-Key"), reqs)
	if err != nil {
		a.failBatch(w, err)
		return
	}
	items, code, longest := make([]batchItem, len(done)), http.StatusOK, 0
	for i, d := range done {
		if d.Err == nil {
			items[i] = batchItem{ID: d.ID, Suppressed: d.Suppressed}
			continue
		}
		p, wait := a.problemOf(d.ID, d.Err)
		items[i], code = batchItem{Error: &p}, http.StatusMultiStatus
		if n, _ := strconv.Atoi(wait); n > longest {
			longest = n
		}
	}
	if longest > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(longest))
	}
	write(w, code, map[string]any{"data": items})
}

// failBatch answers a batch that was refused whole, naming the email at
// fault by its place.
func (a *app) failBatch(w http.ResponseWriter, err error) {
	var whole *mail.BatchInvalid
	if !errors.As(err, &whole) {
		a.failSend(w, "", err)
		return
	}
	p, _ := a.problemOf("", whole.Err)
	if whole.Index >= 0 {
		p.Message = fmt.Sprintf("Email %d of the batch: %s Nothing was sent.", whole.Index+1, p.Message)
	}
	write(w, p.StatusCode, p)
}
