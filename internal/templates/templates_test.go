package templates_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
	"github.com/mettjs/cairnpost/internal/templates"
)

var ctx = context.Background()

func service(max int) *templates.Service {
	return templates.NewService(memstore.New(), templates.Options{Max: max, MaxBytes: 1000})
}

var welcome = templates.New{Name: "welcome", Subject: "Hello {{name}}", HTML: "<p>{{name}}, your code is {{code}}</p>",
	Text: "{{name}}, your code is {{ code }}", Defaults: map[string]string{"name": "there"}}

func TestATemplateIsHeldToWhatAnEmailIs(t *testing.T) {
	s := service(0)
	for field, n := range map[string]templates.New{
		"name":     {Name: "Welcome", Subject: "s", Text: "t"},
		"subject":  {Name: "a", Subject: " ", Text: "t"},
		"html":     {Name: "a", Subject: "s"},
		"text":     {Name: "a", Subject: "s", Text: "{{ a b }}"},
		"defaults": {Name: "a", Subject: "s", Text: "{{a}}", Defaults: map[string]string{"b": "SECRETVALUE"}},
		"domain":   {Name: "a", Subject: "s", Text: "t", Domain: "not a domain"},
	} {
		var bad *templates.Invalid
		if _, err := s.Create(ctx, "", n); !errors.As(err, &bad) || bad.Field != field || strings.Contains(bad.Reason, "SECRETVALUE") {
			t.Errorf("%s: %v", field, err)
		}
	}
	for name, n := range map[string]templates.New{
		"a subject on two lines": {Name: "a", Subject: "s\nBcc: x", Text: "t"},
		"too long":               {Name: "a", Subject: "s", Text: strings.Repeat("x", 1000)},
	} {
		var bad *templates.Invalid
		if _, err := s.Create(ctx, "", n); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var bad *templates.Invalid
	if _, err := s.Create(ctx, "", templates.New{Name: "a", Subject: "{{a}} {{b}} {{", Text: "t"}); !errors.As(err, &bad) ||
		bad.Field != "subject" || !strings.Contains(bad.Reason, "3rd") {
		t.Errorf("which opening: %v", err)
	}
}

func TestATemplateIsItsDomainsOrEveryones(t *testing.T) {
	s := service(2)
	own, err := s.Create(ctx, "a.example", welcome)
	if err != nil || own.Domain != "a.example" {
		t.Fatalf("%+v %v", own, err)
	}
	if _, err := s.Create(ctx, "a.example", welcome); !errors.Is(err, store.ErrTaken) {
		t.Errorf("the same name twice: %v", err)
	}
	other := welcome
	other.Domain = "b.example"
	if _, err := s.Create(ctx, "a.example", other); !errors.Is(err, templates.ErrDomain) {
		t.Errorf("another domain's: %v", err)
	}
	all, err := s.Create(ctx, "", welcome)
	if err != nil || all.Domain != "" {
		t.Fatalf("%+v %v", all, err)
	}
	// A bound key manages its own and is not told of the others.
	if list, err := s.List(ctx, "a.example"); err != nil || len(list) != 1 || list[0].ID != own.ID {
		t.Errorf("%+v %v", list, err)
	}
	if list, err := s.List(ctx, ""); err != nil || len(list) != 2 {
		t.Errorf("%+v %v", list, err)
	}
	if _, err := s.Get(ctx, "a.example", all.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("everyone's, read by a bound key: %v", err)
	}
	if err := s.Delete(ctx, "b.example", own.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted by another domain: %v", err)
	}
	// So many to a domain, and no more.
	second := welcome
	second.Name = "second"
	if _, err := s.Create(ctx, "a.example", second); err != nil {
		t.Fatal(err)
	}
	second.Name = "third"
	var full *templates.Invalid
	if _, err := s.Create(ctx, "a.example", second); !errors.As(err, &full) || !strings.Contains(full.Reason, "has 2") {
		t.Errorf("past the limit: %v", err)
	}
}

func TestAChangeIsHeldToTheSameRules(t *testing.T) {
	s := service(0)
	made, _ := s.Create(ctx, "", welcome)
	text := "Only {{code}}"
	got, err := s.Update(ctx, "", made.ID, templates.Change{Text: &text})
	if err != nil || got.Text != text || got.HTML != welcome.HTML || got.Name != "welcome" {
		t.Fatalf("%+v %v", got, err)
	}
	// What is left must still stand: a default for a variable that is gone.
	subject, html := "Hi", "<p>{{code}}</p>"
	var bad *templates.Invalid
	if _, err := s.Update(ctx, "", made.ID, templates.Change{Subject: &subject, HTML: &html}); !errors.As(err, &bad) || bad.Field != "defaults" {
		t.Fatalf("%v", err)
	}
	none := map[string]string{}
	if _, err := s.Update(ctx, "", made.ID, templates.Change{Subject: &subject, HTML: &html, Defaults: &none}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "", made.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, "", made.ID, templates.Change{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestAnEmailIsFilledFromItsDomainsTemplate(t *testing.T) {
	s := service(0)
	s.Create(ctx, "", welcome)
	mine := welcome
	mine.Subject = "Mine, {{name}}"
	s.Create(ctx, "a.example", mine)
	got, err := s.Fill(ctx, "a.example", "welcome", map[string]string{"code": "<1&2>"})
	if err != nil || got.Subject != "Mine, there" || got.HTML != "<p>there, your code is &lt;1&amp;2&gt;</p>" || got.Text != "there, your code is <1&2>" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := s.Fill(ctx, "b.example", "welcome", map[string]string{"code": "1", "name": "Ana"}); err != nil || got.Subject != "Hello Ana" {
		t.Fatalf("%+v %v", got, err)
	}
	for name, try := range map[string]struct {
		template string
		values   map[string]string
		says     string
	}{
		"no such template":     {"nope", nil, "no template"},
		"a value missing":      {"welcome", nil, `"code"`},
		"a value for nothing":  {"welcome", map[string]string{"code": "1", "cod": "SECRETVALUE"}, `"cod"`},
		"a name that is none":  {"welcome", map[string]string{"code": "1", "SECRET NAME": "x"}, "letters"},
		"an email far too big": {"welcome", map[string]string{"code": strings.Repeat("SECRETVALUE", 100)}, "longer"},
	} {
		var bad *templates.Invalid
		if _, err := s.Fill(ctx, "a.example", try.template, try.values); !errors.As(err, &bad) || bad.Field != "template" ||
			!strings.Contains(bad.Reason, try.says) || strings.Contains(bad.Reason, "SECRET") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestVariablesAreNamedOnceInOrder(t *testing.T) {
	got := templates.Variables(store.Template{Subject: welcome.Subject, HTML: welcome.HTML, Text: welcome.Text, Defaults: welcome.Defaults})
	if len(got) != 2 || got[0] != (templates.Variable{Name: "name", Default: "there"}) || got[1] != (templates.Variable{Name: "code", Required: true}) {
		t.Fatalf("%+v", got)
	}
}
