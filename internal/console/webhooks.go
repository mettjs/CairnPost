package console

import (
	"errors"
	"net/http"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// webhooksView is the list of endpoints and the form to make one.
type webhooksView struct {
	Endpoints []store.Endpoint
	// Bound is the one sending domain the signed-in key manages, "" for all.
	Bound string
	// Made is an endpoint just made, with its signing secret, shown this
	// once; nil otherwise.
	Made       *store.Endpoint
	MadeSecret string
	Problem    string
	Form       hookForm
	Unread     bool
}

// hookForm is an endpoint as a form holds it.
type hookForm struct {
	URL, Domain, Description string
	// All says it hears about every event; otherwise about Types.
	All   bool
	Types []typeChoice
}

// typeChoice is one type of event, ticked or not.
type typeChoice struct {
	Type string
	On   bool
}

// choices is every type of event, those in chosen ticked.
func choices(chosen []string) []typeChoice {
	var out []typeChoice
	for _, t := range webhook.Types() {
		on := false
		for _, c := range chosen {
			on = on || c == t
		}
		out = append(out, typeChoice{t, on})
	}
	return out
}

// hookRead reads an endpoint from a form, and says what is wrong with
// what it chose to hear about, "" for nothing. Every event is a choice
// of its own: its types are then nil. A form that says "every event"
// with events ticked, or "only these" with none, says two things, and
// is asked which.
func hookRead(r *http.Request) (f hookForm, types []string, problem string) {
	hears, ticked := r.PostFormValue("hears"), r.PostForm["events"]
	f = hookForm{URL: typed(r, "url"), Domain: typed(r, "domain"), Description: typed(r, "description"),
		All: hears != "some", Types: choices(ticked)}
	switch {
	case hears == "some" && len(ticked) > 0:
		return f, ticked, ""
	case hears == "some":
		return f, nil, hookProblems["events"]
	case hears == "all" && len(ticked) == 0:
		return f, nil, ""
	case hears == "all":
		return f, nil, `Events are ticked, and "Every event" is chosen. Choose "Only these" to hear about those alone, or untick them.`
	}
	return f, nil, `Choose "Every event" or "Only these".`
}

// webhooksShown is the page with the endpoints the signed-in key manages.
func (c *console) webhooksShown(w http.ResponseWriter, r *http.Request, code int, v webhooksView) {
	v.Bound = who(r).Domain
	list, err := c.Webhooks.List(r.Context(), who(r).Domain)
	if err != nil {
		c.Log.Error("console: the webhook endpoints could not be read", "error", err)
		v.Unread = true
	}
	if v.Endpoints = list; v.Form.Types == nil {
		v.Form.All, v.Form.Types = true, choices(nil)
	}
	c.render(w, r, code, "webhooks", v)
}

func (c *console) webhooksPage(w http.ResponseWriter, r *http.Request) {
	c.webhooksShown(w, r, http.StatusOK, webhooksView{})
}

// createWebhook makes an endpoint and shows its signing secret, in the
// answer to the request that made it and never again.
func (c *console) createWebhook(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		c.webhooksShown(w, r, http.StatusUnprocessableEntity, webhooksView{Problem: "That is more than this form takes."})
		return
	}
	f, types, problem := hookRead(r)
	refuse := func(code int, why string) { c.webhooksShown(w, r, code, webhooksView{Problem: why, Form: f}) }
	if problem != "" {
		refuse(http.StatusUnprocessableEntity, problem)
		return
	}
	e, err := c.Webhooks.Create(r.Context(), who(r).Domain, webhook.New{URL: r.PostFormValue("url"),
		Domain: r.PostFormValue("domain"), Types: types, Description: r.PostFormValue("description")})
	var invalid *webhook.Invalid
	switch {
	case errors.As(err, &invalid):
		refuse(http.StatusUnprocessableEntity, hookProblem(invalid))
	case errors.Is(err, webhook.ErrDomain):
		refuse(http.StatusForbidden, "The key you are signed in with manages one sending domain, and can only make endpoints for it.")
	case err != nil:
		c.Log.Error("console: a webhook endpoint could not be made", "error", err)
		refuse(http.StatusServiceUnavailable, "The endpoint could not be made. Try again shortly.")
	default:
		// Who made which endpoint, by ids: never its URL, which can hold
		// a token, nor its secret.
		c.Log.Info("console: a webhook endpoint was made", "by", who(r).Prefix, "endpoint", e.ID, "domain", e.Domain)
		c.webhooksShown(w, r, http.StatusCreated, webhooksView{Made: &e, MadeSecret: webhook.Reveal(e.Secret)})
	}
}
