package console

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/mettjs/cairnpost/internal/fill"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/templates"
)

// templateForm is a template as a form holds it. Defaults are lines of
// "name = value".
type templateForm struct {
	Name, Domain, Subject, HTML, Text, Defaults string
}

// templateRow is a template in the list, with the variables it names.
type templateRow struct {
	store.Template
	Variables []templates.Variable
}

// templatesView is the list of templates and the form to make one.
type templatesView struct {
	Templates []templateRow
	// Bound is the one sending domain the signed-in key manages, "" for all.
	Bound   string
	Problem string
	Form    templateForm
	Unread  bool
	// Tab is the part shown: "" the list, "new" the form.
	Tab  string
	Tabs tabs
}

// defaultsRead reads lines of "name = value" into the values variables
// are filled with when a send gives none; a problem for a line that is
// neither that nor empty. A value is kept as typed after the "=".
func defaultsRead(typed string) (map[string]string, string) {
	out := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(typed, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if name = strings.TrimSpace(name); !ok || !fill.Name(name) {
			return nil, `Each line of the defaults is a variable's name, "=", and what it is filled with, such as: name = there`
		}
		if _, twice := out[name]; twice {
			return nil, "The defaults name a variable twice. Give each one line."
		}
		out[name] = strings.TrimSpace(value)
	}
	return out, ""
}

// defaultsText writes defaults as the form holds them, by name.
func defaultsText(d map[string]string) string {
	names := make([]string, 0, len(d))
	for name := range d {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name + " = " + d[name] + "\n")
	}
	return b.String()
}

// templateRead reads a template from a form as it was typed: a body is
// kept to the letter, and only what frames it is trimmed.
func templateRead(r *http.Request) templateForm {
	whole := func(field string) string {
		return strings.ToValidUTF8(strings.ReplaceAll(r.PostFormValue(field), "\r\n", "\n"), "")
	}
	return templateForm{Name: typed(r, "name"), Domain: typed(r, "domain"), Subject: typed(r, "subject"),
		HTML: whole("html"), Text: whole("text"), Defaults: whole("defaults")}
}

// templateProblem says, in the form's words, why a template was refused.
func templateProblem(bad *templates.Invalid) string {
	switch bad.Field {
	case "name":
		if strings.Contains(bad.Reason, "as many") {
			return bad.Reason
		}
		return "The name is how a send asks for the template: 1 to 64 of a-z, 0-9, - and _, starting with a letter or a digit."
	case "subject":
		return "A template needs a subject, on one line, of at most 998 bytes."
	case "domain":
		return "The sending domain must be a domain, such as mail.example.com, or left empty."
	}
	return bad.Reason
}

// wideForm reads a form as long as a template may be.
func (c *console) wideForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, c.TemplateBytes*2+8192)
	return r.ParseForm() == nil
}

// templatesShown is the page with the templates the signed-in key manages.
func (c *console) templatesShown(w http.ResponseWriter, r *http.Request, code int, v templatesView) {
	v.Bound = who(r).Domain
	list, err := c.Templates.List(r.Context(), who(r).Domain)
	if err != nil {
		c.Log.Error("console: the templates could not be read", "error", err)
		v.Unread = true
	}
	for _, t := range list {
		v.Templates = append(v.Templates, templateRow{t, templates.Variables(t)})
	}
	forced := ""
	if v.Problem != "" {
		forced = "new"
	}
	v.Tab = tabOf(r, forced, "new")
	v.Tabs = tabsOf("Templates", "/templates", nil, v.Tab, tab{Label: "Templates", N: len(list)}, tab{Key: "new", Label: "New template"})
	c.render(w, r, code, "templates", v)
}

func (c *console) templatesPage(w http.ResponseWriter, r *http.Request) {
	c.templatesShown(w, r, http.StatusOK, templatesView{})
}

// refusal says why a template was not kept, and with what status: ok
// when it was. The log never holds what a template says.
func (c *console) refusal(err error, doing string) (code int, why string, ok bool) {
	var invalid *templates.Invalid
	switch {
	case err == nil:
		return 0, "", true
	case errors.As(err, &invalid):
		return http.StatusUnprocessableEntity, templateProblem(invalid), false
	case errors.Is(err, templates.ErrDomain):
		return http.StatusForbidden, "The key you are signed in with manages one sending domain, and can only make templates for it.", false
	case errors.Is(err, store.ErrTaken):
		return http.StatusConflict, "That sending domain already has a template of this name. Give this one another name.", false
	}
	c.Log.Error("console: a template could not be "+doing, "error", err)
	return http.StatusServiceUnavailable, "Try again shortly.", false
}

// createTemplate makes a template and leads to its page.
func (c *console) createTemplate(w http.ResponseWriter, r *http.Request) {
	if !c.wideForm(w, r) {
		c.templatesShown(w, r, http.StatusUnprocessableEntity, templatesView{Problem: "That is longer than a template may be."})
		return
	}
	f := templateRead(r)
	refuse := func(code int, why string) { c.templatesShown(w, r, code, templatesView{Problem: why, Form: f}) }
	defaults, problem := defaultsRead(f.Defaults)
	if problem != "" {
		refuse(http.StatusUnprocessableEntity, problem)
		return
	}
	t, err := c.Templates.Create(r.Context(), who(r).Domain, templates.New{Name: f.Name, Domain: f.Domain,
		Subject: f.Subject, HTML: f.HTML, Text: f.Text, Defaults: defaults})
	if code, why, ok := c.refusal(err, "made"); !ok {
		refuse(code, why)
		return
	}
	c.Log.Info("console: a template was made", "by", who(r).Prefix, "template", t.ID, "domain", t.Domain)
	c.flash(w, "template-made")
	http.Redirect(w, r, templateAt(t.ID, ""), http.StatusSeeOther)
}
