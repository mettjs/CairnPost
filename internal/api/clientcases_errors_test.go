package api

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// saidInAnother are the error names no case can name by a call's own
// refusal, and why.
var saidInAnother = map[string]string{
	"not_started":       "said of one email inside a batch's answer, never as the answer",
	"invalid_signature": "answered to POST /inbound, which no client calls",
	"inbound_off":       "answered to POST /inbound, which no client calls",
	"inbound_full":      "answered to POST /inbound, which no client calls",
}

// The router answers a method a path does not take before any call is
// reached, so no call's description names it.
const routersOwn = "method_not_allowed"

var (
	namesSaid = regexp.MustCompile("`name` is one of: ([^.]*)\\.")
	aName     = regexp.MustCompile("`([a-z_]+)`")
)

// The cases put every error name the API describes in front of the
// clients, each as the answer, with the status, of a call the API
// describes it for; and name none the API does not have.
func TestTheClientCasesNameTheErrorsTheAPIAnswers(t *testing.T) {
	var file struct {
		Cases []struct {
			Name string `json:"name"`
			Call string `json:"call"`
			Want struct {
				Status int     `json:"status"`
				Code   *string `json:"code"`
			} `json:"want"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("../../client-cases/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	doc, calls := described(t), clientCalls(t)
	ops := operations(doc)
	covered := map[string]bool{}
	for _, c := range file.Cases {
		call, ok := calls[c.Call]
		if !ok {
			t.Errorf("%s: %s is no call of calls.json", c.Name, c.Call)
		}
		if c.Want.Code == nil {
			continue
		}
		covered[*c.Want.Code] = true
		answers, _ := ops[call.Route]["responses"].(node)
		said, _ := resolved(doc, answers[strconv.Itoa(c.Want.Status)]).(node)
		text, _ := said["description"].(string)
		if !strings.Contains(text, "`"+*c.Want.Code+"`") && *c.Want.Code != routersOwn {
			t.Errorf("%s: the API does not describe a %d %s for %s", c.Name, c.Want.Status, *c.Want.Code, call.Route)
		}
	}
	// Every name the file that describes the API gives for a refusal.
	text, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]bool{routersOwn: true}
	for _, list := range namesSaid.FindAllStringSubmatch(string(text), -1) {
		for _, name := range aName.FindAllStringSubmatch(list[1], -1) {
			all[name[1]] = true
		}
	}
	if len(all) < 20 {
		t.Fatalf("only %d error names were read out of openapi.json", len(all))
	}
	for name := range all {
		if !covered[name] && saidInAnother[name] == "" {
			t.Errorf("no client case answers %s", name)
		}
	}
	for name := range covered {
		if !all[name] || saidInAnother[name] != "" {
			t.Errorf("a client case answers %s, which the API does not describe as a call's refusal", name)
		}
	}
}
