package main

import (
	"encoding/json"
	"testing"
)

func TestWhatTheSendCommandAsksOfATemplate(t *testing.T) {
	use, err := templateOf("welcome", []string{"code=4821", "note=a=b", "empty="})
	raw, _ := json.Marshal(use)
	if err != nil || string(raw) != `{"name":"welcome","variables":{"code":"4821","empty":"","note":"a=b"}}` {
		t.Fatalf("%s %v", raw, err)
	}
	if use, err := templateOf("", nil); use != nil || err != nil {
		t.Fatalf("no template: %v %v", use, err)
	}
	for name, try := range map[string][]string{"a value with no template": {"", "a=b"}, "no name": {"welcome", "=b"}, "no value": {"welcome", "code"}} {
		if _, err := templateOf(try[0], try[1:]); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
}
