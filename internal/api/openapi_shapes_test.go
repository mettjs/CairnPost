package api

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/page"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// shape writes a schema short: a reference by its name, a list as
// "[]" and what it holds, an object by its fields.
func shape(s node) string {
	if ref, ok := s["$ref"].(string); ok {
		return ref[strings.LastIndex(ref, "/")+1:]
	}
	if forms, ok := s["oneOf"].([]any); ok {
		var parts []string
		for _, one := range forms {
			parts = append(parts, shape(one.(node)))
		}
		return strings.Join(parts, "|")
	}
	if items, ok := s["items"].(node); ok {
		return "[]" + shape(items)
	}
	if props, ok := s["properties"].(node); ok {
		var parts []string
		for name, p := range props {
			parts = append(parts, name+":"+shape(p.(node)))
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ",") + "}"
	}
	kind, _ := s["type"].(string)
	return kind
}

// carried writes what a body is: its schema for JSON, else its media type.
func carried(body node) string {
	content, _ := body["content"].(node)
	for media, c := range content {
		if media != "application/json" {
			return media
		}
		return shape(c.(node)["schema"].(node))
	}
	return ""
}

// calls is what each call takes and what it answers when it works, by
// status, written from the handlers.
var calls = map[string][2]string{
	"getHealth":             {"", "200 Health"},
	"getLimits":             {"", "200 {driver:string,quota:Quota|null}"},
	"getMetrics":            {"", "200 text/plain"},
	"sendEmail":             {"SendEmail", "200 Sent"},
	"sendBatch":             {"[]SendEmail", "200 {data:[]BatchItem}; 207 {data:[]BatchItem}"},
	"getEmail":              {"", "200 Email"},
	"rescheduleEmail":       {"Reschedule", "200 Sent"},
	"cancelEmail":           {"", "200 {id:string,state:string}"},
	"getEmailAttachment":    {"", "200 application/octet-stream"},
	"listCapturedEmails":    {"", "200 {data:[]CapturedEmail}"},
	"listSuppressions":      {"", "200 {data:[]Suppression,next:string}"},
	"addSuppression":        {"AddSuppression", "200 {added:boolean,address:string,domain:string}; 201 {added:boolean,address:string,domain:string}"},
	"liftSuppressions":      {"LiftSuppressions", "200 {lifted:integer}"},
	"receiveEmail":          {"message/rfc822", "200 {id:string}; 201 {id:string}"},
	"listReceived":          {"", "200 {data:[]Received,next:string}"},
	"getReceived":           {"", "200 Received"},
	"deleteReceived":        {"", "204 "},
	"getReceivedRaw":        {"", "200 application/octet-stream"},
	"getReceivedAttachment": {"", "200 application/octet-stream"},
	"createTemplate":        {"NewTemplate", "201 Template"},
	"listTemplates":         {"", "200 {data:[]Template,more:boolean}"},
	"getTemplate":           {"", "200 Template"},
	"updateTemplate":        {"ChangeTemplate", "200 Template"},
	"deleteTemplate":        {"", "204 "},
	"createWebhook":         {"NewEndpoint", "201 Endpoint"},
	"listWebhooks":          {"", "200 {data:[]Endpoint}"},
	"getWebhook":            {"", "200 Endpoint"},
	"updateWebhook":         {"ChangeEndpoint", "200 Endpoint"},
	"deleteWebhook":         {"", "204 "},
	"listDeliveries":        {"", "200 {data:[]Delivery,next:string}"},
	"retryDelivery":         {"", "200 {state:string}"},
	"testWebhook":           {"", "200 {delivered:boolean,failure:string,status:integer}"},
	"rotateWebhookSecret":   {"", "200 Endpoint"},
	"resumeWebhook":         {"", "200 Endpoint"},
}

// Each call takes and answers what its handler does.
func TestEachCallTakesAndAnswersItsOwn(t *testing.T) {
	for pattern, o := range operations(described(t)) {
		id := o["operationId"].(string)
		want, ok := calls[id]
		if !ok {
			t.Errorf("%s (%s): the test does not know what it takes and answers", pattern, id)
			continue
		}
		body, _ := o["requestBody"].(node)
		var got []string
		for code, r := range o["responses"].(node) {
			if !strings.HasPrefix(code, "2") {
				continue
			}
			if ref, ok := r.(node)["$ref"].(string); ok {
				got = append(got, code+" "+ref)
				continue
			}
			got = append(got, code+" "+carried(r.(node)))
		}
		sort.Strings(got)
		if takes, answers := carried(body), strings.Join(got, "; "); takes != want[0] || answers != want[1] {
			t.Errorf("%s: the file says it takes %q and answers %q; the handler takes %q and answers %q", pattern, takes, answers, want[0], want[1])
		}
	}
}

// fits says how a schema differs from the Go type it describes, "" for
// not at all. A field read in more than one form (oneOf) is left alone.
func fits(s node, of reflect.Type) string {
	for of.Kind() == reflect.Pointer {
		of = of.Elem()
	}
	if _, many := s["oneOf"]; many {
		return ""
	}
	if ref, ok := s["$ref"].(string); ok {
		name := ref[strings.LastIndex(ref, "/")+1:]
		if v, known := answers[name]; !known || reflect.TypeOf(v) != of {
			return fmt.Sprintf("refers to %s, the code has %s", name, of)
		}
		return ""
	}
	want := "object"
	switch of.Kind() {
	case reflect.String:
		want = "string"
	case reflect.Bool:
		want = "boolean"
	case reflect.Int, reflect.Int64:
		want = "integer"
	case reflect.Slice:
		want = "array"
	}
	if of == reflect.TypeFor[time.Time]() {
		want = "string"
		if s["format"] != "date-time" {
			return "is a time and must say date-time"
		}
	}
	if s["type"] != want {
		return fmt.Sprintf("is %v, the code has %s", s["type"], want)
	}
	if want == "array" {
		items, _ := s["items"].(node)
		return fits(items, of.Elem())
	}
	return ""
}

// Each field is the kind of thing the code reads or writes there.
func TestEachFieldIsItsKind(t *testing.T) {
	schemas := described(t)["components"].(node)["schemas"].(node)
	var check func(where string, schema node, of reflect.Type)
	check = func(where string, schema node, of reflect.Type) {
		for of.Kind() == reflect.Pointer || of.Kind() == reflect.Slice {
			of = of.Elem()
		}
		props, _ := schema["properties"].(node)
		for _, f := range fieldsOf(of) {
			p, _ := props[f.name].(node)
			if p == nil {
				continue // a missing field is another test's to say
			}
			if why := fits(p, f.kind); why != "" {
				t.Errorf("%s.%s %s", where, f.name, why)
			}
			inner := p
			if items, ok := p["items"].(node); ok {
				inner = items
			}
			if _, inline := inner["properties"]; inline {
				check(where+"."+f.name, inner, f.kind)
			}
		}
	}
	for _, set := range []map[string]any{answers, takes} {
		for name, v := range set {
			check(name, schemas[name].(node), reflect.TypeOf(v))
		}
	}
}

// The values the file lists are the ones the code has.
func TestTheListedValuesAreTheCodes(t *testing.T) {
	schemas := described(t)["components"].(node)["schemas"].(node)
	at := func(name string, path ...string) node {
		s := schemas[name].(node)
		for _, part := range path {
			s = s[part].(node)
		}
		return s
	}
	fails := []string{webhook.FailTimeout, webhook.FailRefused, webhook.FailDNS, webhook.FailTLS, webhook.FailBlocked, webhook.FailStatus, webhook.FailOther}
	events := []string{store.EventDelivered, store.EventDeferred, store.EventBounced, store.EventComplained, store.EventFailed}
	states := []string{store.StateScheduled, store.StateSending, store.StateSent, store.StateDeferred, store.StateDelivered,
		store.StateBounced, store.StateComplained, store.StateFailed, store.StateUnknown, store.StateCancelled}
	for _, c := range []struct {
		where string
		in    node
		want  []string
	}{
		{"Delivery.state", at("Delivery", "properties", "state"), store.DeliveryStates},
		{"Delivery.last_failure", at("Delivery", "properties", "last_failure"), fails},
		{"Email.state", at("Email", "properties", "state"), states},
		{"Email.reason", at("Email", "properties", "reason"), store.HeldReasons},
		{"Event.type", at("Event", "properties", "type"), events},
		{"Recipient.kind", at("Recipient", "properties", "kind"), []string{store.KindTo, store.KindCc, store.KindBcc}},
		{"Suppression.reason", at("Suppression", "properties", "reason"), store.Reasons},
		{"LiftSuppressions.reason", at("LiftSuppressions", "properties", "reason"), store.Reasons},
		{"Endpoint.events", at("Endpoint", "properties", "events", "items"), webhook.Types()},
		{"NewEndpoint.events", at("NewEndpoint", "properties", "events", "items"), webhook.Types()},
		{"ChangeEndpoint.events", at("ChangeEndpoint", "properties", "events", "items"), webhook.Types()},
	} {
		var got []string
		listed, _ := c.in["enum"].([]any)
		for _, v := range listed {
			got = append(got, v.(string))
		}
		want := slices.Clone(c.want)
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: the file lists %v, the code has %v", c.where, got, want)
		}
	}
}

// The page sizes the file gives are the rule's.
func TestThePageSizesAreTheRules(t *testing.T) {
	n := 0
	for pattern, o := range operations(described(t)) {
		params, _ := o["parameters"].([]any)
		for _, p := range params {
			if p := p.(node); p["name"] == "limit" {
				n++
				if s := p["schema"].(node); s["default"] != float64(page.Default) || s["maximum"] != float64(page.Most) {
					t.Errorf("%s: a page is %v, %v at most; the rule is %d, %d at most", pattern, s["default"], s["maximum"], page.Default, page.Most)
				}
			}
		}
	}
	if n != 3 {
		t.Errorf("%d lists take a limit, want 3", n)
	}
}
