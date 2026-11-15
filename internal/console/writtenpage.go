package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/templates"
)

// templateView is one template: what it says, how an email is asked
// for from it, and the form that changes it.
type templateView struct {
	Template  store.Template
	Variables []templates.Variable
	// Example is a send that uses it, as the API takes one.
	Example string
	Problem string
	Form    templateForm
	// Tab is the part shown: "" what it says, "edit" the form.
	Tab  string
	Tabs tabs
}

// templateAt is a template's page, at a part of it if that is one.
func templateAt(id, tab string) string {
	q := url.Values{}
	if tab == "edit" {
		q.Set("tab", tab)
	}
	return at("/templates/"+url.PathEscape(id), q)
}

// written answers the template a request names, if the signed-in key
// manages it; otherwise it has answered the request, and says false.
// Another domain's template is, to this key, not there.
func (c *console) written(w http.ResponseWriter, r *http.Request) (store.Template, bool) {
	t, err := c.Templates.Get(r.Context(), who(r).Domain, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such template.")
	case err != nil:
		c.Log.Error("console: a template could not be read", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The template could not be read. Try again shortly.")
	}
	return t, err == nil
}

// exampleOf is a send made from a template, with a value to give for
// each variable that has no default.
func exampleOf(t store.Template, variables []templates.Variable) string {
	domain := t.Domain
	if domain == "" {
		domain = "example.com"
	}
	type use struct {
		Name      string            `json:"name"`
		Variables map[string]string `json:"variables,omitempty"`
	}
	values := map[string]string{}
	for _, v := range variables {
		if v.Required {
			values[v.Name] = "..."
		}
	}
	// Names and a domain: nothing here fails to encode.
	out, _ := json.MarshalIndent(struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Template use    `json:"template"`
	}{"you@" + domain, "someone@example.org", use{t.Name, values}}, "", "  ")
	return string(out)
}

// formOfTemplate is a template as its form holds it.
func formOfTemplate(t store.Template) templateForm {
	return templateForm{Name: t.Name, Subject: t.Subject, HTML: t.HTML, Text: t.Text, Defaults: defaultsText(t.Defaults)}
}

// templateShown is a template's page.
func (c *console) templateShown(w http.ResponseWriter, r *http.Request, code int, v templateView) {
	forced := ""
	if v.Problem != "" {
		forced = "edit"
	} else {
		v.Form = formOfTemplate(v.Template)
	}
	v.Variables = templates.Variables(v.Template)
	v.Example = exampleOf(v.Template, v.Variables)
	v.Tab = tabOf(r, forced, "edit")
	v.Tabs = tabsOf("Template", "/templates/"+url.PathEscape(v.Template.ID), nil, v.Tab, tab{Label: "What it says"}, tab{Key: "edit", Label: "Change it"})
	c.render(w, r, code, "template", v)
}

func (c *console) templatePage(w http.ResponseWriter, r *http.Request) {
	if t, ok := c.written(w, r); ok {
		c.templateShown(w, r, http.StatusOK, templateView{Template: t})
	}
}

// updateTemplate changes a template: every email asked for from now on
// is made from what it says now.
func (c *console) updateTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := c.written(w, r)
	if !ok {
		return
	}
	if !c.wideForm(w, r) {
		c.templateShown(w, r, http.StatusUnprocessableEntity, templateView{Template: t, Problem: "That is longer than a template may be.",
			Form: formOfTemplate(t)})
		return
	}
	f := templateRead(r)
	refuse := func(code int, why string) {
		c.templateShown(w, r, code, templateView{Template: t, Problem: why, Form: f})
	}
	defaults, problem := defaultsRead(f.Defaults)
	if problem != "" {
		refuse(http.StatusUnprocessableEntity, problem)
		return
	}
	_, err := c.Templates.Update(r.Context(), who(r).Domain, t.ID, templates.Change{Name: &f.Name, Subject: &f.Subject,
		HTML: &f.HTML, Text: &f.Text, Defaults: &defaults})
	if errors.Is(err, store.ErrNotFound) {
		c.fail(w, r, http.StatusNotFound, "There is no such template.")
		return
	}
	if code, why, ok := c.refusal(err, "changed"); !ok {
		refuse(code, why)
		return
	}
	c.Log.Info("console: a template was changed", "by", who(r).Prefix, "template", t.ID)
	c.flash(w, "template-changed")
	http.Redirect(w, r, templateAt(t.ID, ""), http.StatusSeeOther)
}

// discardPage asks before a template is deleted.
func (c *console) discardPage(w http.ResponseWriter, r *http.Request) {
	if t, ok := c.written(w, r); ok {
		c.render(w, r, http.StatusOK, "discard", t)
	}
}

// discardTemplate deletes a template.
func (c *console) discardTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := c.Templates.Delete(r.Context(), who(r).Domain, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.fail(w, r, http.StatusNotFound, "There is no such template.")
	case err != nil:
		c.Log.Error("console: a template could not be deleted", "error", err)
		c.fail(w, r, http.StatusServiceUnavailable, "The template could not be deleted. Try again shortly.")
	default:
		c.Log.Info("console: a template was deleted", "by", who(r).Prefix, "template", id)
		c.flash(w, "template-deleted")
		http.Redirect(w, r, at("/templates", nil), http.StatusSeeOther)
	}
}
