package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/inbound"
	"github.com/mettjs/cairnpost/internal/mail"
)

type node = map[string]any

// described reads the file that describes the API to other programs.
func described(t *testing.T) node {
	t.Helper()
	raw, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc node
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.json is not JSON: %v", err)
	}
	return doc
}

var methods = []string{"get", "post", "put", "patch", "delete", "head", "options"}

// operations answers every call the file describes, by "GET /path".
func operations(doc node) map[string]node {
	out := map[string]node{}
	for path, item := range doc["paths"].(node) {
		for _, m := range methods {
			if o, ok := item.(node)[m].(node); ok {
				out[strings.ToUpper(m)+" "+path] = o
			}
		}
	}
	return out
}

// The file describes every call the API has and none it has not, and
// asks of each the key the API asks.
func TestTheFileDescribesEveryRoute(t *testing.T) {
	ops := operations(described(t))
	want := map[string]string{KeyNone: `[]`, KeyAny: `[{"key":[]}]`, KeyAdmin: `[{"key":["admin"]}]`}
	for _, r := range Routes() {
		o, ok := ops[r.Pattern]
		if !ok {
			t.Errorf("%s is not in openapi.json", r.Pattern)
			continue
		}
		delete(ops, r.Pattern)
		got, _ := json.Marshal(o["security"])
		if string(got) != want[r.Key] {
			t.Errorf("%s: security is %s, the route needs %s", r.Pattern, got, want[r.Key])
		}
	}
	for pattern := range ops {
		t.Errorf("openapi.json describes %s, which is no route", pattern)
	}
}

var named = regexp.MustCompile(`\{([a-z]+)\}`)

// Each call declares the values its path names, and no others; has a
// name of its own; and says what it answers.
func TestEachCallIsWhole(t *testing.T) {
	seen := map[string]string{}
	for pattern, o := range operations(described(t)) {
		id, _ := o["operationId"].(string)
		if id == "" || seen[id] != "" {
			t.Errorf("%s: operationId %q is missing or also names %s", pattern, id, seen[id])
		}
		seen[id] = pattern
		var want, got []string
		for _, m := range named.FindAllStringSubmatch(pattern, -1) {
			want = append(want, m[1])
		}
		params, _ := o["parameters"].([]any)
		for _, p := range params {
			if p := p.(node); p["in"] == "path" {
				if p["required"] != true {
					t.Errorf("%s: path value %v is not required", pattern, p["name"])
				}
				got = append(got, p["name"].(string))
			}
		}
		sort.Strings(want)
		sort.Strings(got)
		if !slices.Equal(want, got) {
			t.Errorf("%s: declares the path values %v, the path names %v", pattern, got, want)
		}
		answers, _ := o["responses"].(node)
		ok := false
		for code := range answers {
			ok = ok || strings.HasPrefix(code, "2")
		}
		if !ok {
			t.Errorf("%s: says no answer for when it works", pattern)
		}
	}
}

// Every reference in the file leads somewhere.
func TestEveryReferenceLeadsSomewhere(t *testing.T) {
	doc := described(t)
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, e := range v {
				walk(e)
			}
		case node:
			if ref, ok := v["$ref"].(string); ok {
				var at any = doc
				for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					next, _ := at.(node)
					if at = next[part]; at == nil {
						t.Errorf("%s leads nowhere", ref)
						break
					}
				}
			}
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(doc)
}

// field is one JSON field of a struct.
type field struct {
	name     string
	required bool
	kind     reflect.Type
}

// fieldsOf answers a struct's JSON fields, those of a struct it embeds
// among them. One that is never left out is required.
func fieldsOf(t reflect.Type) []field {
	var out []field
	for i := range t.NumField() {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")
		if f.Anonymous && tag[0] == "" {
			out = append(out, fieldsOf(f.Type)...)
			continue
		}
		if tag[0] == "-" || !f.IsExported() {
			continue
		}
		if tag[0] == "" {
			tag[0] = f.Name // written under its own name
		}
		out = append(out, field{tag[0], !slices.Contains(tag, "omitempty") && !slices.Contains(tag, "omitzero"), f.Type})
	}
	return out
}

// holds checks a schema against the struct it describes: the same
// fields, and for an answer the same ones required. A struct written
// inside another is checked with it.
func holds(t *testing.T, where string, schema node, of reflect.Type, answer bool) {
	t.Helper()
	props, _ := schema["properties"].(node)
	var want, wantRequired, got, gotRequired []string
	for _, f := range fieldsOf(of) {
		want = append(want, f.name)
		if f.required {
			wantRequired = append(wantRequired, f.name)
		}
		inner := f.kind
		for inner.Kind() == reflect.Pointer || inner.Kind() == reflect.Slice {
			inner = inner.Elem()
		}
		sub, _ := props[f.name].(node)
		if items, ok := sub["items"].(node); ok {
			sub = items
		}
		if _, inline := sub["properties"]; inline && inner.Kind() == reflect.Struct {
			holds(t, where+"."+f.name, sub, inner, answer)
		}
	}
	for name := range props {
		got = append(got, name)
	}
	required, _ := schema["required"].([]any)
	for _, name := range required {
		gotRequired = append(gotRequired, name.(string))
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(want, got) {
		t.Errorf("%s: the file has the fields %v, the code %v", where, got, want)
	}
	if !answer {
		// What a call takes is read strictly: an unknown field is refused.
		if schema["additionalProperties"] != false {
			t.Errorf("%s: must say additionalProperties false, as the API refuses a field it does not know", where)
		}
		for _, name := range gotRequired {
			if !slices.Contains(want, name) {
				t.Errorf("%s: requires %q, which is no field", where, name)
			}
		}
		return
	}
	sort.Strings(wantRequired)
	sort.Strings(gotRequired)
	if !slices.Equal(wantRequired, gotRequired) {
		t.Errorf("%s: the file requires %v, the code always writes %v", where, gotRequired, wantRequired)
	}
}

// answers are the types the API writes, by the schema that describes
// each; takes the types it reads.
var answers = map[string]any{
	"Problem": problem{}, "Sent": sentView{}, "ItemProblem": itemProblem{}, "BatchItem": batchItem{},
	"Email": emailView{}, "Recipient": recipientView{}, "Event": eventView{}, "File": mail.File{},
	"Content": mail.Content{}, "CapturedEmail": CapturedEmail{}, "Quota": Quota{},
	"Suppression": suppressionView{}, "Variable": variableView{}, "Template": templateView{},
	"Endpoint": endpointView{}, "Delivery": deliveryView{}, "Header": inbound.Header{},
	"ReceivedFile": inbound.Part{}, "ReceivedContent": inbound.Read{}, "Received": inboundView{},
}

var takes = map[string]any{
	"SendEmail": sendBody{}, "Reschedule": rescheduleBody{}, "AddSuppression": suppressBody{},
	"LiftSuppressions": liftBody{}, "NewTemplate": templateBody{}, "ChangeTemplate": templateChange{},
	"NewEndpoint": endpointBody{}, "ChangeEndpoint": endpointChange{},
}

// The shapes the file gives are the shapes the code reads and writes.
func TestTheShapesAreTheCodes(t *testing.T) {
	schemas := described(t)["components"].(node)["schemas"].(node)
	for name, v := range answers {
		holds(t, name, schemas[name].(node), reflect.TypeOf(v), true)
	}
	for name, v := range takes {
		holds(t, name, schemas[name].(node), reflect.TypeOf(v), false)
	}
	// Health is written as a map, and so is held to a real answer.
	rec := httptest.NewRecorder()
	a := &app{Deps: Deps{Ping: func(context.Context) error { return nil }}}
	a.health(rec, httptest.NewRequest("GET", "/health", nil))
	var said map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &said); err != nil {
		t.Fatal(err)
	}
	health := schemas["Health"].(node)
	var want, got, required []string
	for name := range said {
		want = append(want, name)
	}
	for name := range health["properties"].(node) {
		got = append(got, name)
	}
	for _, name := range health["required"].([]any) {
		required = append(required, name.(string))
	}
	sort.Strings(want)
	sort.Strings(got)
	sort.Strings(required)
	if !slices.Equal(want, got) || !slices.Equal(want, required) {
		t.Errorf("Health: the file has %v and requires %v, the answer has %v", got, required, want)
	}
	for name := range schemas {
		if answers[name] == nil && takes[name] == nil && name != "Health" {
			t.Errorf("the schema %s is held to no type", name)
		}
	}
}
