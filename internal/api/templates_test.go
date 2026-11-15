package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

const welcome = `{"name":"welcome","subject":"Hello {{name}}","html":"<p>{{name}}, your code is {{code}}</p>",
	"text":"{{name}}, your code is {{code}}","defaults":{"name":"there"}}`

func TestATemplateIsMadeReadChangedAndDeletedByAnAdminKey(t *testing.T) {
	r := newRig(t)
	if code, _, _ := r.do("POST", "/templates", r.send, "", welcome); code != 403 {
		t.Fatalf("made by a send key: %d", code)
	}
	code, _, made := r.do("POST", "/templates", r.admin, "", welcome)
	variables, _ := made["variables"].([]any)
	if code != 201 || made["name"] != "welcome" || made["domain"] != nil || len(variables) != 2 {
		t.Fatalf("%d %v", code, made)
	}
	if first, _ := variables[0].(map[string]any); first["name"] != "name" || first["default"] != "there" || first["required"] != false {
		t.Errorf("%v", variables)
	}
	if second, _ := variables[1].(map[string]any); second["name"] != "code" || second["default"] != nil || second["required"] != true {
		t.Errorf("%v", variables)
	}
	id := made["id"].(string)
	if code, name, _ := r.do("POST", "/templates", r.admin, "", welcome); code != 409 || name != "name_taken" {
		t.Errorf("the same name twice: %d %s", code, name)
	}
	if code, name, out := r.do("POST", "/templates", r.admin, "", `{"name":"x","subject":"s","text":"{{ }}"}`); code != 422 ||
		name != "validation_error" || !strings.Contains(out["message"].(string), "1st") {
		t.Errorf("a text that cannot be filled: %d %v", code, out)
	}
	if code, _, _ := r.do("POST", "/templates", r.admin, "", `{"name":"x","subject":"s","text":"t","from":"a@example.com"}`); code != 400 && code != 422 {
		t.Errorf("a field that is not known: %d", code)
	}
	// A list says what each is, without its bodies.
	_, _, list := r.do("GET", "/templates", r.admin, "", "")
	data, _ := list["data"].([]any)
	if one, _ := data[0].(map[string]any); len(data) != 1 || one["subject"] != "Hello {{name}}" || one["html"] != nil || one["text"] != nil {
		t.Errorf("%v", list)
	}
	if code, _, got := r.do("GET", "/templates/"+id, r.admin, "", ""); code != 200 || got["html"] != "<p>{{name}}, your code is {{code}}</p>" {
		t.Errorf("%d %v", code, got)
	}
	code, _, changed := r.do("PATCH", "/templates/"+id, r.admin, "", `{"subject":"Hi {{name}}"}`)
	if code != 200 || changed["subject"] != "Hi {{name}}" || changed["text"] != "{{name}}, your code is {{code}}" {
		t.Errorf("%d %v", code, changed)
	}
	// Another domain's admin is not told it is there.
	bound, _, err := r.keys.Create(context.Background(), "other", store.ScopeAdmin, "other.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, try := range [][2]string{{"GET", ""}, {"PATCH", `{"subject":"x"}`}, {"DELETE", ""}} {
		if code, _, _ := r.do(try[0], "/templates/"+id, bound, "", try[1]); code != 404 {
			t.Errorf("%s by another domain's admin: %d", try[0], code)
		}
	}
	if code, name, _ := r.do("POST", "/templates", bound, "", strings.Replace(welcome, `{"name"`, `{"domain":"example.com","name"`, 1)); code != 403 || name != "domain_not_allowed" {
		t.Errorf("made for another domain: %d %s", code, name)
	}
	if code, _, own := r.do("POST", "/templates", bound, "", welcome); code != 201 || own["domain"] != "other.example" {
		t.Errorf("made for its own: %d %v", code, own)
	}
	if code, _, _ := r.do("DELETE", "/templates/"+id, r.admin, "", ""); code != 204 {
		t.Errorf("deleted: %d", code)
	}
	if code, _, _ := r.do("GET", "/templates/"+id, r.admin, "", ""); code != 404 {
		t.Errorf("read once deleted: %d", code)
	}
	// So many, and no more: the rig allows three that serve every domain.
	for _, name := range []string{"a", "b", "c"} {
		if code, _, _ := r.do("POST", "/templates", r.admin, "", `{"name":"`+name+`","subject":"s","text":"t"}`); code != 201 {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if code, name, _ := r.do("POST", "/templates", r.admin, "", `{"name":"d","subject":"s","text":"t"}`); code != 422 || name != "validation_error" {
		t.Errorf("past the limit: %d %s", code, name)
	}
}

func TestAnEmailIsSentFromATemplate(t *testing.T) {
	r := newRig(t)
	r.do("POST", "/templates", r.admin, "", welcome)
	send := func(idem, more string) (int, string, map[string]any) {
		return r.do("POST", "/emails", r.send, idem, `{"from":"app@example.com","to":"ana@example.org",`+more+`}`)
	}
	code, _, sent := send("k1", `"template":{"name":"welcome","variables":{"code":4821,"name":"<Ana>"}}`)
	if code != 200 || len(r.drv.held) != 1 {
		t.Fatalf("%d %v", code, sent)
	}
	if m := r.drv.held[0]; m.Subject != "Hello <Ana>" || m.HTML != "<p>&lt;Ana&gt;, your code is 4821</p>" || m.Text != "<Ana>, your code is 4821" {
		t.Fatalf("%+v", m)
	}
	// A subject given with it is the email's; a default fills what is not given.
	if code, _, _ := send("", `"subject":"Your code","template":{"name":"welcome","variables":{"code":"12.50"}}`); code != 200 ||
		r.drv.held[1].Subject != "Your code" || r.drv.held[1].Text != "there, your code is 12.50" {
		t.Fatalf("%d %+v", code, r.drv.held[1])
	}
	for name, try := range map[string][2]string{
		"with a body":         {`"text":"t","template":{"name":"welcome","variables":{"code":1}}`, `"html"`},
		"no such template":    {`"template":{"name":"nope"}`, "no template"},
		"a value missing":     {`"template":{"name":"welcome"}`, `\"code\"`},
		"a value for nothing": {`"template":{"name":"welcome","variables":{"code":1,"cod":"SECRETVALUE"}}`, `\"cod\"`},
	} {
		code, kind, out := send("", try[0])
		said, _ := out["message"].(string)
		if code != 422 || kind != "validation_error" || !strings.Contains(said, strings.ReplaceAll(try[1], `\"`, `"`)) || strings.Contains(said, "SECRETVALUE") {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	for name, body := range map[string]string{"a value that is a list": `"template":{"name":"welcome","variables":{"code":[1]}}`,
		"a value that is null": `"template":{"name":"welcome","variables":{"code":null}}`,
		"a field not known":    `"template":{"name":"welcome","id":"x"}`} {
		if code, _, _ := send("", body); code < 400 || code >= 500 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if len(r.drv.held) != 2 {
		t.Fatalf("%d emails went", len(r.drv.held))
	}
	// The same request made again is the same email, whatever the
	// template says by then; a different value is a different request.
	_, _, list := r.do("GET", "/templates", r.admin, "", "")
	id := list["data"].([]any)[0].(map[string]any)["id"].(string)
	if code, _, _ := r.do("PATCH", "/templates/"+id, r.admin, "", `{"subject":"Changed {{name}}"}`); code != 200 {
		t.Fatal(code)
	}
	if code, _, again := send("k1", `"template":{"name":"welcome","variables":{"name":"<Ana>","code":4821}}`); code != 200 ||
		again["id"] != sent["id"] || len(r.drv.held) != 2 {
		t.Errorf("the repeat: %d %v, %d emails", code, again, len(r.drv.held))
	}
	if code, name, _ := send("k1", `"template":{"name":"welcome","variables":{"name":"<Ana>","code":4822}}`); code != 409 || name != "invalid_idempotent_request" {
		t.Errorf("another value under the same key: %d %s", code, name)
	}
}

func TestATemplateIsItsDomainsFirstAndABoundKeyLearnsOfNoOther(t *testing.T) {
	r := newRig(t)
	r.do("POST", "/templates", r.admin, "", `{"name":"note","subject":"Everyone's","text":"t"}`)
	r.do("POST", "/templates", r.admin, "", `{"name":"note","domain":"a.example","subject":"A's","text":"t"}`)
	r.do("POST", "/templates", r.admin, "", `{"name":"only-a","domain":"a.example","subject":"A's","text":"t"}`)
	body := func(from, name string) string {
		return `{"from":"app@` + from + `","to":"ana@example.org","template":{"name":"` + name + `"}}`
	}
	r.do("POST", "/emails", r.send, "", body("a.example", "note"))
	r.do("POST", "/emails", r.send, "", body("b.example", "note"))
	if len(r.drv.held) != 2 || r.drv.held[0].Subject != "A's" || r.drv.held[1].Subject != "Everyone's" {
		t.Fatalf("%+v", r.drv.held)
	}
	if code, _, _ := r.do("POST", "/emails", r.send, "", body("b.example", "only-a")); code != 422 {
		t.Errorf("another domain's template: %d", code)
	}
	bound, _, err := r.keys.Create(context.Background(), "b", store.ScopeSend, "b.example")
	if err != nil {
		t.Fatal(err)
	}
	// Whether the template is there or not, the answer is the same.
	for _, name := range []string{"only-a", "nope"} {
		if code, kind, _ := r.do("POST", "/emails", bound, "", body("a.example", name)); code != 403 || kind != "domain_not_allowed" {
			t.Errorf("%s from a domain the key may not send from: %d %s", name, code, kind)
		}
	}
}

func TestABatchAndALaterEmailAreFilledWhenTheyAreAsked(t *testing.T) {
	r := newRig(t)
	r.do("POST", "/templates", r.admin, "", welcome)
	one := func(more string) string {
		return `{"from":"app@example.com","to":"ana@example.org","template":{"name":"welcome","variables":{` + more + `}}}`
	}
	code, _, out := r.do("POST", "/emails/batch", r.send, "", "["+one(`"code":1`)+","+one(`"code":2,"name":"Bo"`)+"]")
	if code != 200 || len(r.drv.held) != 2 || r.drv.held[0].Text+r.drv.held[1].Text != "there, your code is 1Bo, your code is 2" {
		t.Fatalf("%d %v %+v", code, out, r.drv.held)
	}
	// One that cannot be filled, and none goes.
	if code, _, out := r.do("POST", "/emails/batch", r.send, "", "["+one(`"code":1`)+","+one(``)+"]"); code != 422 || len(r.drv.held) != 2 {
		t.Fatalf("%d %v", code, out)
	}
	// An email that waits keeps what it was filled with.
	later := strings.Replace(one(`"code":3`), `{"from"`, `{"scheduled_at":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`","from"`, 1)
	code, _, waits := r.do("POST", "/emails", r.send, "", later)
	if code != 200 || waits["scheduled_at"] == nil {
		t.Fatalf("%d %v", code, waits)
	}
	_, _, list := r.do("GET", "/templates", r.admin, "", "")
	r.do("DELETE", "/templates/"+list["data"].([]any)[0].(map[string]any)["id"].(string), r.admin, "", "")
	_, _, got := r.do("GET", "/emails/"+waits["id"].(string), r.admin, "", "")
	if content, _ := got["content"].(map[string]any); content["subject"] != "Hello there" || content["text"] != "there, your code is 3" {
		t.Fatalf("%v", got)
	}
}
