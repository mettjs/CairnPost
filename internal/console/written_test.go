package console_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
)

func welcomeForm() url.Values {
	return url.Values{"name": {"welcome"}, "subject": {"Hello {{name}}"}, "html": {"<p>{{name}}, your code is {{code}}</p>"},
		"text": {"{{name}},\r\nyour code is {{code}}"}, "defaults": {"name = there\r\n"}}
}

// templateID is the id in the address a made template's form led to.
func templateID(t *testing.T, location string) string {
	t.Helper()
	const before = "/console/templates/"
	if !strings.HasPrefix(location, before) {
		t.Fatalf("led to %q", location)
	}
	return strings.TrimPrefix(location, before)
}

func TestATemplateIsMadeShownChangedAndDeleted(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	if page := b.do("GET", "/templates", nil).Body.String(); !strings.Contains(page, "There are no templates yet.") {
		t.Fatalf("the empty list: %s", page)
	}
	rec := b.do("POST", "/templates", welcomeForm())
	if rec.Code != 303 {
		t.Fatalf("made: %d %s", rec.Code, rec.Body)
	}
	id := templateID(t, rec.Header().Get("Location"))
	page := b.do("GET", "/templates/"+id, nil).Body.String()
	for _, want := range []string{"The template was made.", "Hello {{name}}", "&lt;p&gt;{{name}}, your code is {{code}}&lt;/p&gt;",
		"Filled with <q>there</q>", "The send is refused", `&#34;name&#34;: &#34;welcome&#34;`, `&#34;code&#34;: &#34;...&#34;`} {
		if !strings.Contains(page, want) {
			t.Errorf("the template's page lacks %q", want)
		}
	}
	// A body is kept as it was typed, with one kind of line end.
	if got, _ := r.s.Template(context.Background(), id); got.Text != "{{name}},\nyour code is {{code}}" || got.Defaults["name"] != "there" {
		t.Errorf("%+v", got)
	}
	if list := b.do("GET", "/templates", nil).Body.String(); !strings.Contains(list, `/console/templates/`+id+`">welcome</a>`) ||
		!strings.Contains(list, "<code>code</code>") {
		t.Errorf("the list: %s", list)
	}
	// A change that cannot stand comes back on its own part, as typed.
	bad := welcomeForm()
	bad.Set("html", "<p>{{ not a name }}</p>")
	rec = b.do("POST", "/templates/"+id, bad)
	if page := rec.Body.String(); rec.Code != 422 || !strings.Contains(page, "The change was not saved.") ||
		!strings.Contains(page, "{{ not a name }}") || !strings.Contains(page, "1st") {
		t.Errorf("a refused change: %d %s", rec.Code, page)
	}
	good := welcomeForm()
	good.Set("subject", "Hi {{name}}")
	if page := b.follow("POST", "/templates/"+id, good).Body.String(); !strings.Contains(page, "The template was changed.") || !strings.Contains(page, "Hi {{name}}") {
		t.Errorf("a change: %s", page)
	}
	if page := b.do("GET", "/templates/"+id+"/delete", nil).Body.String(); !strings.Contains(page, "Delete this template?") {
		t.Errorf("asked first: %s", page)
	}
	if page := b.follow("POST", "/templates/"+id+"/delete", url.Values{}).Body.String(); !strings.Contains(page, "The template was deleted.") ||
		!strings.Contains(page, "There are no templates yet.") {
		t.Errorf("deleted: %s", page)
	}
	if rec := b.do("GET", "/templates/"+id, nil); rec.Code != 404 {
		t.Errorf("read once deleted: %d", rec.Code)
	}
	// What a template says is in no log line.
	for _, mark := range []string{"Hello {{name}}", "your code is"} {
		if strings.Contains(r.logged.String(), mark) {
			t.Errorf("the log holds %q", mark)
		}
	}
}

func TestATemplateThatCannotBeMadeComesBackAsTyped(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	for name, try := range map[string]struct {
		change [2]string
		says   string
		code   int
	}{
		"a name with capitals":   {[2]string{"name", "Welcome"}, "Small letters", 422},
		"no subject":             {[2]string{"subject", ""}, "needs a subject", 422},
		"a default that is none": {[2]string{"defaults", "just words"}, "Each line of the defaults", 422},
		"a default for nothing":  {[2]string{"defaults", "nobody = x"}, "does not name", 422},
		"a domain that is none":  {[2]string{"domain", "not a domain"}, "must be a domain", 422},
		"a subject's variable":   {[2]string{"subject", "Hi {{ first name }}"}, "In the subject, the", 422},
		"a subject with no gap":  {[2]string{"subject", strings.Repeat("x", 950)}, "The subject must have a space", 422},
	} {
		f := welcomeForm()
		f.Set(try.change[0], try.change[1])
		rec := b.do("POST", "/templates", f)
		page := rec.Body.String()
		if rec.Code != try.code || !strings.Contains(page, "The template was not made.") || !strings.Contains(page, "your code is {{code}}") {
			t.Errorf("%s: %d", name, rec.Code)
		}
		if try.says != "Small letters" && !strings.Contains(page, try.says) {
			t.Errorf("%s: the page does not say %q", name, try.says)
		}
	}
	b.do("POST", "/templates", welcomeForm())
	if rec := b.do("POST", "/templates", welcomeForm()); rec.Code != 409 || !strings.Contains(rec.Body.String(), "already has a template of this name") {
		t.Errorf("the same name twice: %d", rec.Code)
	}
	long := welcomeForm()
	long.Set("html", strings.Repeat("x", 30000))
	if rec := b.do("POST", "/templates", long); rec.Code != 422 || !strings.Contains(rec.Body.String(), "longer than a template may be") {
		t.Errorf("far too long: %d", rec.Code)
	}
	// As long as a template may be, in letters a form writes at length.
	wide := welcomeForm()
	wide.Set("name", "wide")
	wide.Set("html", strings.Repeat("\n<й>", 300)+"{{code}}")
	if rec := b.do("POST", "/templates", wide); rec.Code != 303 {
		t.Errorf("a template of letters a form writes at length: %d", rec.Code)
	}
}

func TestABoundKeySeesAndMakesItsOwnDomainsTemplates(t *testing.T) {
	r := newRig(t, nil)
	all := r.signedIn(r.admin)
	id := templateID(t, all.do("POST", "/templates", welcomeForm()).Header().Get("Location"))
	bound, _, err := r.ks.Create(context.Background(), "a", store.ScopeAdmin, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	b := r.signedIn(bound)
	if page := b.do("GET", "/templates", nil).Body.String(); !strings.Contains(page, "There are no templates yet.") || !strings.Contains(page, "<code>a.example</code>") {
		t.Errorf("another's template is listed: %s", page)
	}
	for _, try := range [][2]string{{"GET", "/templates/" + id}, {"GET", "/templates/" + id + "/delete"}, {"POST", "/templates/" + id + "/delete"}, {"POST", "/templates/" + id}} {
		form := url.Values(nil)
		if try[0] == "POST" {
			form = welcomeForm()
		}
		if rec := b.do(try[0], try[1], form); rec.Code != 404 {
			t.Errorf("%s %s: %d", try[0], try[1], rec.Code)
		}
	}
	other := welcomeForm()
	other.Set("domain", "b.example")
	if rec := b.do("POST", "/templates", other); rec.Code != 403 {
		t.Errorf("made for another domain: %d", rec.Code)
	}
	own := templateID(t, b.do("POST", "/templates", welcomeForm()).Header().Get("Location"))
	if got, _ := r.s.Template(context.Background(), own); got.Domain != "a.example" {
		t.Errorf("%+v", got)
	}
}

// What the API kept, the form shows and saves back as it was.
func TestDefaultsComeBackFromTheFormAsTheyWere(t *testing.T) {
	r := newRig(t, nil)
	b := r.signedIn(r.admin)
	f := welcomeForm()
	f.Set("defaults", "name =  two spaces before, one after \r\ncode = a=b")
	id := templateID(t, b.do("POST", "/templates", f).Header().Get("Location"))
	read := func() map[string]string {
		got, _ := r.s.Template(context.Background(), id)
		return got.Defaults
	}
	if d := read(); d["name"] != " two spaces before, one after " || d["code"] != "a=b" {
		t.Fatalf("%q", d)
	}
	// Saved again as the form shows it, nothing of them changes.
	page := b.do("GET", "/templates/"+id+"?tab=edit", nil).Body.String()
	if !strings.Contains(page, "code = a=b\nname =  two spaces before, one after \n") {
		t.Fatalf("the form shows: %s", page)
	}
	again := welcomeForm()
	again.Set("defaults", "code = a=b\nname =  two spaces before, one after \n")
	if rec := b.do("POST", "/templates/"+id, again); rec.Code != 303 {
		t.Fatalf("saved again: %d", rec.Code)
	}
	if d := read(); d["name"] != " two spaces before, one after " || d["code"] != "a=b" {
		t.Fatalf("after a save: %q", d)
	}
	// A change far too long comes back with the template as it is kept.
	long := welcomeForm()
	long.Set("text", strings.Repeat("x", 30000))
	rec := b.do("POST", "/templates/"+id, long)
	if page := rec.Body.String(); rec.Code != 422 || !strings.Contains(page, "longer than a template may be") ||
		!strings.Contains(page, "your code is {{code}}") || !strings.Contains(page, "code = a=b") {
		t.Fatalf("a change too long: %d", rec.Code)
	}
}
