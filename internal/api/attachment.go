package api

import (
	"errors"
	"net/http"

	"github.com/mettjs/cairnpost/internal/download"
	"github.com/mettjs/cairnpost/internal/store"
)

// getAttachment is GET /emails/{id}/attachments/{place}: what one of an
// email's files held, by its place from 1 in the email's content. It is
// there while the email waits to be sent, and afterwards only where the
// operator keeps what emails say and their files, while they are kept.
// Admin key.
func (a *app) getAttachment(w http.ResponseWriter, r *http.Request) {
	f, err := a.Mail.FileFor(r.Context(), callerOf(r).key, r.PathValue("id"), download.Place(r.PathValue("place")))
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "There is no such file: no email has this id, it has no file at this place, or what the file held is not kept.")
	case err != nil:
		a.Log.Error("api: an email's file could not be read", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The file could not be read. Try again shortly.")
	default:
		download.Serve(w, f.Filename, f.Content)
	}
}
