package api

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// clientCases reads one of the files every client is held to
// (client-cases). They say what this API takes and answers, so they are
// held to it here: a client that passes them works against this server.
func clientCases(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile("../../client-cases/" + name)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// answerCase is an answer a case has a server give.
type answerCase struct {
	Status  int               `json:"status"`
	JSON    json.RawMessage   `json:"json"`
	Text    *string           `json:"text"`
	Headers map[string]string `json:"headers"`
}

// clientCall is a call as calls.json describes it.
type clientCall struct {
	Name  string `json:"name"`
	Route string `json:"route"`
	Key   string `json:"key"`
	Sends struct {
		Method         string            `json:"method"`
		Path           string            `json:"path"`
		Query          map[string]string `json:"query"`
		IdempotencyKey string            `json:"idempotency_key"`
		Body           json.RawMessage   `json:"body"`
	} `json:"sends"`
	Answer answerCase `json:"answer"`
}

// clientCalls answers the calls of calls.json by name.
func clientCalls(t *testing.T) map[string]clientCall {
	t.Helper()
	var file struct {
		Calls []clientCall `json:"calls"`
	}
	clientCases(t, "calls.json", &file)
	out := map[string]clientCall{}
	for _, c := range file.Calls {
		if _, twice := out[c.Name]; twice {
			t.Errorf("%s is described twice", c.Name)
		}
		out[c.Name] = c
	}
	return out
}

// notForClients are the routes no client calls, and why.
var notForClients = map[string]string{
	"POST /inbound": "emails are handed in by whoever receives them, signed, not by a client",
	"GET /metrics":  "read by a metrics collector, as text",
}

// The cases name every call a client makes and none the API has not; each
// asks for the key its route asks, sends what the route takes, and is
// answered as the API describes.
func TestTheClientCasesDescribeTheRoutes(t *testing.T) {
	keys := map[string]string{}
	for _, r := range Routes() {
		keys[r.Pattern] = r.Key
	}
	doc := described(t)
	ops := operations(doc)
	called := map[string]bool{}
	for _, c := range clientCalls(t) {
		called[c.Route] = true
		key, ok := keys[c.Route]
		if !ok {
			t.Errorf("%s: %s is no route", c.Name, c.Route)
			continue
		}
		method, path, _ := strings.Cut(c.Route, " ")
		// The path the route takes, with anything but a slash where it names a value.
		takes := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(named.ReplaceAllString(path, "\x00")), "\x00", "[^/]+") + "$")
		if c.Key != key || c.Sends.Method != method || !takes.MatchString(c.Sends.Path) {
			t.Errorf("%s: key %s, %s %s; the route is %s and needs the key %q", c.Name, c.Key, c.Sends.Method, c.Sends.Path, c.Route, key)
		}
		wrong := append(sent(doc, ops[c.Route], c), answered(doc, ops[c.Route], c.Answer, c.Name)...)
		for _, w := range wrong {
			t.Error(w)
		}
	}
	for pattern := range keys {
		if !called[pattern] && notForClients[pattern] == "" {
			t.Errorf("no client case calls %s", pattern)
		}
	}
	for pattern := range notForClients {
		if called[pattern] || keys[pattern] == "" {
			t.Errorf("%s is listed as not for clients, and is called or is no route", pattern)
		}
	}
}

// An answer a client must take is one the API gives to that call. (One it
// must refuse is, by its nature, not.)
func TestTheAnswersAClientMustTakeAreTheAPIs(t *testing.T) {
	var file struct {
		Refused json.RawMessage `json:"refused"`
		Taken   []struct {
			Name   string     `json:"name"`
			Calls  []string   `json:"calls"`
			Answer answerCase `json:"answer"`
		} `json:"taken"`
	}
	clientCases(t, "answers.json", &file)
	doc, calls := described(t), clientCalls(t)
	ops := operations(doc)
	if len(file.Taken) == 0 {
		t.Fatal("answers.json holds no answer to take")
	}
	for _, c := range file.Taken {
		if len(c.Calls) == 0 {
			t.Errorf("%s: is put in front of no call", c.Name)
		}
		for _, name := range c.Calls {
			call, ok := calls[name]
			if !ok {
				t.Errorf("%s: %s is no call of calls.json", c.Name, name)
				continue
			}
			for _, w := range answered(doc, ops[call.Route], c.Answer, c.Name+", for "+name) {
				t.Error(w)
			}
		}
	}
}
