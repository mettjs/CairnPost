package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/templates"
)

type variableView struct {
	Name string `json:"name"`
	// Default is what it is filled with when a send gives it no value;
	// absent for one a send must give.
	Default  *string `json:"default,omitempty"`
	Required bool    `json:"required"`
}

type templateView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Domain is the sending domain it belongs to; absent for one that
	// serves every domain.
	Domain  string `json:"domain,omitempty"`
	Subject string `json:"subject"`
	// HTML and Text are left out of a list.
	HTML      string         `json:"html,omitempty"`
	Text      string         `json:"text,omitempty"`
	Variables []variableView `json:"variables"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func templateOf(t store.Template) templateView {
	variables := []variableView{}
	for _, v := range templates.Variables(t) {
		view := variableView{Name: v.Name, Required: v.Required}
		if !v.Required {
			view.Default = &v.Default
		}
		variables = append(variables, view)
	}
	return templateView{ID: t.ID, Name: t.Name, Domain: t.Domain, Subject: t.Subject, HTML: t.HTML, Text: t.Text,
		Variables: variables, CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC()}
}

// failTemplate answers a template request that was not carried out.
func (a *app) failTemplate(w http.ResponseWriter, err error) {
	var invalid *templates.Invalid
	switch {
	case errors.As(err, &invalid):
		fail(w, http.StatusUnprocessableEntity, "validation_error", invalid.Reason)
	case errors.Is(err, templates.ErrDomain):
		fail(w, http.StatusForbidden, "domain_not_allowed", "This API key manages the templates of its own sending domain only.")
	case errors.Is(err, store.ErrTaken):
		fail(w, http.StatusConflict, "name_taken", "A template of this name is already there for that sending domain. Change it, or give this one another name.")
	case errors.Is(err, store.ErrStale):
		fail(w, http.StatusConflict, "changed_meanwhile", "The template was changed by others while this change was made. Read it and try again.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "There is no such template.")
	default:
		a.Log.Error("api: a template request failed", "error", err)
		fail(w, http.StatusServiceUnavailable, "unavailable", "The templates could not be reached. Try again shortly.")
	}
}

// createTemplate is POST /templates: keep an email to send many times.
func (a *app) createTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string            `json:"name"`
		Domain   string            `json:"domain"`
		Subject  string            `json:"subject"`
		HTML     string            `json:"html"`
		Text     string            `json:"text"`
		Defaults map[string]string `json:"defaults"`
	}
	if !a.body(w, r, &body, `The body must be JSON with "name", "subject" and "html" or "text"; "domain" and "defaults" are optional.`) {
		return
	}
	t, err := a.Templates.Create(r.Context(), scope(r), templates.New{Name: body.Name, Domain: body.Domain,
		Subject: body.Subject, HTML: body.HTML, Text: body.Text, Defaults: body.Defaults})
	if err != nil {
		a.failTemplate(w, err)
		return
	}
	write(w, http.StatusCreated, templateOf(t))
}

// listTemplates is GET /templates: each without its bodies. A key bound
// to no domain may ask for one domain's with ?domain=.
func (a *app) listTemplates(w http.ResponseWriter, r *http.Request) {
	list, more, err := a.Templates.List(r.Context(), scope(r), r.URL.Query().Get("domain"))
	if err != nil {
		a.failTemplate(w, err)
		return
	}
	data := make([]templateView, 0, len(list))
	for _, t := range list {
		data = append(data, templateOf(t))
	}
	// more says the list is cut short: ask for one domain's.
	write(w, http.StatusOK, map[string]any{"data": data, "more": more})
}

// getTemplate is GET /templates/{id}.
func (a *app) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := a.Templates.Get(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		a.failTemplate(w, err)
		return
	}
	write(w, http.StatusOK, templateOf(t))
}

// updateTemplate is PATCH /templates/{id}: what is named is changed,
// the rest stays. An email asked for from now on is made from it.
func (a *app) updateTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     *string            `json:"name"`
		Subject  *string            `json:"subject"`
		HTML     *string            `json:"html"`
		Text     *string            `json:"text"`
		Defaults *map[string]string `json:"defaults"`
	}
	if !a.body(w, r, &body, `The body must be JSON with any of "name", "subject", "html", "text" and "defaults".`) {
		return
	}
	t, err := a.Templates.Update(r.Context(), scope(r), r.PathValue("id"), templates.Change{Name: body.Name,
		Subject: body.Subject, HTML: body.HTML, Text: body.Text, Defaults: body.Defaults})
	if err != nil {
		a.failTemplate(w, err)
		return
	}
	write(w, http.StatusOK, templateOf(t))
}

// deleteTemplate is DELETE /templates/{id}.
func (a *app) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := a.Templates.Delete(r.Context(), scope(r), r.PathValue("id")); err != nil {
		a.failTemplate(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
