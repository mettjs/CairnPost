package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/download"
	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/store"
)

// receive is POST /inbound: an email handed on by the mail provider for
// an address of a sending domain. It carries no API key: the headers it
// comes with are signed under the secret this server and whoever hands
// emails on both hold, and are checked before any of the email is read.
// The body is the email as it came.
func (a *app) receive(w http.ResponseWriter, r *http.Request) {
	h := r.Header
	e := inbound.Envelope{Timestamp: h.Get(inbound.HeaderTimestamp), To: h.Get(inbound.HeaderTo), From: h.Get(inbound.HeaderFrom),
		Digest: h.Get(inbound.HeaderDigest), Signature: h.Get(inbound.HeaderSignature)}
	if !a.failReceive(w, a.Inbound.Admit(e)) {
		return
	}
	// Held in memory while it is kept: only so much at one time.
	most := a.Inbound.MaxBytes()
	release, ok := a.uploads.take(r.ContentLength, a.MaxBodyBytes, most, a.MaxUploadBytes)
	if !ok {
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "unavailable", "The server is taking as many large emails as it can hold. Try again shortly.")
		return
	}
	defer release()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, most))
	if !a.read(w, err, "The body must be the email as it came.") {
		return
	}
	id, again, err := a.Inbound.Receive(r.Context(), e, raw)
	if !a.failReceive(w, err) {
		return
	}
	status := http.StatusCreated
	if again {
		status = http.StatusOK // handed on before: kept once
	}
	write(w, status, map[string]string{"id": id})
}

// failReceive answers a hand-over that was not taken, and says whether
// it was.
func (a *app) failReceive(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, inbound.ErrOff):
		fail(w, http.StatusNotFound, "inbound_off", "This server takes no emails: it has no CAIRNPOST_INBOUND_SECRET.")
	case errors.Is(err, inbound.ErrUnsigned):
		fail(w, http.StatusUnauthorized, "invalid_signature", "The email was not handed on by the holder of this server's inbound secret, or too long ago.")
	case errors.Is(err, inbound.ErrAddress):
		fail(w, http.StatusUnprocessableEntity, "validation_error", "What the email came for is not an address.")
	case errors.Is(err, inbound.ErrTooLarge):
		fail(w, http.StatusRequestEntityTooLarge, "request_too_large", "The email is longer than this server takes.")
	case errors.Is(err, inbound.ErrFull):
		w.Header().Set("Retry-After", "300")
		fail(w, http.StatusServiceUnavailable, "inbound_full", "The emails kept hold as much as this server keeps. The email was not kept.")
	default:
		a.Log.Error("api: an email that came could not be kept", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The email could not be kept. Try again shortly.")
	}
	return false
}

type inboundView struct {
	ID string `json:"id"`
	// To is the address it came for; From who it came from, as the
	// sending server said, absent for a bounce.
	To         string    `json:"to"`
	From       string    `json:"from,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	// KeptUntil is when it stops being shown, and is deleted soon after.
	KeptUntil time.Time `json:"kept_until"`
	Size      int       `json:"size"`
	// Content is the email as it reads; left out of a list.
	Content *inbound.Read `json:"content,omitempty"`
}

func inboundOf(in store.Inbound) inboundView {
	return inboundView{ID: in.ID, To: in.Recipient, From: in.Sender, ReceivedAt: in.ReceivedAt.UTC(), KeptUntil: in.ExpiresAt.UTC(), Size: in.Bytes}
}

// failInbound answers a request about an email that came.
func (a *app) failInbound(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found", "There is no such email: none that came has this id, or its time is up.")
		return
	}
	a.Log.Error("api: an email that came could not be read", "error", err)
	fail(w, http.StatusServiceUnavailable, "unavailable", "The emails that came could not be reached. Try again shortly.")
}

// listInbound is GET /inbound: the emails that came and are still kept,
// newest first, without what they say. ?limit= sizes the page and
// ?after= takes the "next" of the page before. Admin key.
func (a *app) listInbound(w http.ResponseWriter, r *http.Request) {
	limit, ok := limitOf(w, r)
	if !ok {
		return
	}
	list, next, err := a.Inbound.List(r.Context(), callerOf(r).key, r.URL.Query().Get("after"), limit)
	if err != nil {
		a.failInbound(w, err)
		return
	}
	data := make([]inboundView, 0, len(list))
	for _, in := range list {
		data = append(data, inboundOf(in))
	}
	out := map[string]any{"data": data}
	if next != "" {
		out["next"] = next
	}
	write(w, http.StatusOK, out)
}

// getInbound is GET /inbound/{id}: an email that came, as it reads.
// Admin key.
func (a *app) getInbound(w http.ResponseWriter, r *http.Request) {
	in, read, err := a.Inbound.Read(r.Context(), callerOf(r).key, r.PathValue("id"))
	if err != nil {
		a.failInbound(w, err)
		return
	}
	view := inboundOf(in)
	view.Content = &read
	write(w, http.StatusOK, view)
}

// getInboundRaw is GET /inbound/{id}/raw: the email as it came, as a
// file to save. Admin key.
func (a *app) getInboundRaw(w http.ResponseWriter, r *http.Request) {
	in, raw, err := a.Inbound.Raw(r.Context(), callerOf(r).key, r.PathValue("id"))
	if err != nil {
		a.failInbound(w, err)
		return
	}
	download.Serve(w, in.ID+".eml", raw)
}

// getInboundFile is GET /inbound/{id}/attachments/{place}: one of its
// files, by its place from 1, as a file to save. Admin key.
func (a *app) getInboundFile(w http.ResponseWriter, r *http.Request) {
	part, content, err := a.Inbound.FileOf(r.Context(), callerOf(r).key, r.PathValue("id"), download.Place(r.PathValue("place")))
	if err != nil {
		a.failInbound(w, err)
		return
	}
	download.Serve(w, part.Filename, content)
}

// deleteInbound is DELETE /inbound/{id}: delete it before its time.
// Admin key.
func (a *app) deleteInbound(w http.ResponseWriter, r *http.Request) {
	if err := a.Inbound.Delete(r.Context(), callerOf(r).key, r.PathValue("id")); err != nil {
		a.failInbound(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
