package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/api"
	"github.com/mettjs/cairnpost/internal/store"
)

func TestWebhookEndpointsAreAnAdminsToManage(t *testing.T) {
	r := newRig(t)
	answered := http.StatusNoContent
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(answered)
		w.Write([]byte("WORDS-OF-THE-ENDPOINT"))
	}))
	defer app.Close()
	make := `{"url":"` + app.URL + `/hook","domain":"example.com","events":["email.bounced"],"description":"billing"}`
	for _, call := range [][2]string{{"POST", "/webhooks"}, {"GET", "/webhooks"}, {"GET", "/webhooks/x"}, {"PATCH", "/webhooks/x"},
		{"DELETE", "/webhooks/x"}, {"GET", "/webhooks/x/deliveries"}, {"POST", "/webhooks/x/test"}, {"POST", "/webhooks/x/deliveries/y/retry"},
		{"POST", "/webhooks/x/secret"}, {"POST", "/webhooks/x/resume"}} {
		if code, name, _ := r.do(call[0], call[1], r.send, "", make); code != 403 || name != "admin_key_required" {
			t.Fatalf("%s %s with a send key: %d %s", call[0], call[1], code, name)
		}
	}
	code, _, made := r.do("POST", "/webhooks", r.admin, "", make)
	id, _ := made["id"].(string)
	secret, _ := made["secret"].(string)
	if code != 201 || id == "" || !strings.HasPrefix(secret, "whsec_") || made["enabled"] != true || made["domain"] != "example.com" {
		t.Fatalf("create: %d %v", code, made)
	}
	// The secret is said once. No later answer carries it.
	for _, path := range []string{"/webhooks", "/webhooks/" + id} {
		code, _, got := r.do("GET", path, r.admin, "", "")
		if code != 200 || strings.Contains(r.raw("GET", path, r.admin), secret) || strings.Contains(r.raw("GET", path, r.admin), "secret") {
			t.Fatalf("GET %s: %d %v", path, code, got)
		}
	}
	code, _, got := r.do("PATCH", "/webhooks/"+id, r.admin, "", `{"enabled":false,"events":[]}`)
	if events, _ := got["events"].([]any); code != 200 || got["enabled"] != false || len(events) != 0 || got["description"] != "billing" || got["secret"] != nil {
		t.Fatalf("change: %d %v", code, got)
	}
	// Trying it out says how the endpoint answered, and none of its words.
	code, _, got = r.do("POST", "/webhooks/"+id+"/test", r.admin, "", "")
	if code != 200 || got["delivered"] != true || got["status"] != float64(204) {
		t.Fatalf("test: %d %v", code, got)
	}
	answered = http.StatusInternalServerError
	code, _, got = r.do("POST", "/webhooks/"+id+"/test", r.admin, "", "")
	if code != 200 || got["delivered"] != false || got["status"] != float64(500) || got["failure"] != "status" ||
		strings.Contains(r.raw("POST", "/webhooks/"+id+"/test", r.admin), "WORDS") {
		t.Fatalf("test of a failing endpoint: %d %v", code, got)
	}
	for what, c := range map[string]struct {
		method, path, body string
		code               int
		name               string
	}{
		"a url that is none":   {"POST", "/webhooks", `{"url":"app.example.com"}`, 422, "validation_error"},
		"an unknown event":     {"POST", "/webhooks", `{"url":"https://app.example.com/h","events":["email.opened"]}`, 422, "validation_error"},
		"an unknown field":     {"POST", "/webhooks", `{"url":"https://app.example.com/h","secret":"mine"}`, 422, "validation_error"},
		"an id that is none":   {"GET", "/webhooks/nothing", "", 404, "not_found"},
		"a change of nothing":  {"PATCH", "/webhooks/nothing", `{"enabled":true}`, 404, "not_found"},
		"a bad limit":          {"GET", "/webhooks/" + id + "/deliveries?limit=0", "", 422, "validation_error"},
		"a delivery not there": {"POST", "/webhooks/" + id + "/deliveries/nothing/retry", "", 404, "not_found"},
	} {
		if code, name, _ := r.do(c.method, c.path, r.admin, "", c.body); code != c.code || name != c.name {
			t.Errorf("%s: %d %s", what, code, name)
		}
	}
	if code, _, got := r.do("GET", "/webhooks/"+id+"/deliveries?state=pending&limit=5", r.admin, "", ""); code != 200 || got["data"] == nil || got["next"] != nil {
		t.Fatalf("deliveries: %d %v", code, got)
	}
	if code, name, _ := r.do("GET", "/webhooks/"+id+"/deliveries?state=lost", r.admin, "", ""); code != 422 || name != "validation_error" {
		t.Fatalf("deliveries in a state that is none: %d %s", code, name)
	}
	if code, _, _ := r.do("DELETE", "/webhooks/"+id, r.admin, "", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := r.do("GET", "/webhooks/"+id, r.admin, "", ""); code != 404 {
		t.Fatalf("once deleted: %d", code)
	}
}

func TestAKeyBoundToADomainManagesOnlyItsOwnEndpoints(t *testing.T) {
	var bound string
	r := newRigWith(t, func(d *api.Deps) {
		var err error
		if bound, _, err = d.Keys.Create(context.Background(), "tenant", store.ScopeAdmin, "example.com"); err != nil {
			t.Fatal(err)
		}
	})
	_, _, theirs := r.do("POST", "/webhooks", r.admin, "", `{"url":"https://app.other.example/h","domain":"other.example"}`)
	other, _ := theirs["id"].(string)
	if code, name, _ := r.do("POST", "/webhooks", bound, "", `{"url":"https://app.example.com/h","domain":"other.example"}`); code != 403 || name != "domain_not_allowed" {
		t.Fatalf("making another domain's: %d %s", code, name)
	}
	code, _, mine := r.do("POST", "/webhooks", bound, "", `{"url":"https://app.example.com/h"}`)
	if code != 201 || mine["domain"] != "example.com" {
		t.Fatalf("its own: %d %v", code, mine)
	}
	if code, _, _ := r.do("GET", "/webhooks/"+other, bound, "", ""); code != 404 {
		t.Fatalf("reading another domain's: %d", code)
	}
	_, _, got := r.do("GET", "/webhooks", bound, "", "")
	if list, _ := got["data"].([]any); len(list) != 1 {
		t.Fatalf("its list: %v", got)
	}
}

// A signing secret is changed by an admin key that manages the endpoint:
// the new one is said once, with until when the old one signs as well.
func TestASigningSecretIsChangedAndSaidOnce(t *testing.T) {
	var (
		bound  string
		logged bytes.Buffer
	)
	r := newRigWith(t, func(d *api.Deps) {
		var err error
		if bound, _, err = d.Keys.Create(context.Background(), "tenant", store.ScopeAdmin, "example.com"); err != nil {
			t.Fatal(err)
		}
		d.Log = slog.New(slog.NewTextHandler(&logged, nil))
	})
	_, _, made := r.do("POST", "/webhooks", r.admin, "", `{"url":"https://app.other.example/h","domain":"other.example"}`)
	id, _ := made["id"].(string)
	old, _ := made["secret"].(string)
	if made["previous_secret_until"] != nil || made["paused_at"] != nil || made["failing_since"] != nil {
		t.Fatalf("a new endpoint: %v", made)
	}
	code, _, got := r.do("POST", "/webhooks/"+id+"/secret", r.admin, "", "")
	fresh, _ := got["secret"].(string)
	until, _ := got["previous_secret_until"].(string)
	if code != 200 || !strings.HasPrefix(fresh, "whsec_") || fresh == old || until == "" || got["id"] != id {
		t.Fatalf("change: %d %v", code, got)
	}
	if raw := r.raw("GET", "/webhooks/"+id, r.admin); strings.Contains(raw, fresh) || strings.Contains(raw, old) ||
		!strings.Contains(raw, `"previous_secret_until":"`+until+`"`) {
		t.Fatalf("a later read: %s", raw)
	}
	for what, call := range map[string][2]string{"another domain's key": {bound, id}, "no such endpoint": {r.admin, "nothing"}} {
		if code, name, _ := r.do("POST", "/webhooks/"+call[1]+"/secret", call[0], "", ""); code != 404 || name != "not_found" {
			t.Errorf("%s: %d %s", what, code, name)
		}
	}
	// Resuming one that is not paused answers it as it is; a paused one
	// is resumed, and a change that says "enabled" does not do that.
	if code, _, got := r.do("POST", "/webhooks/"+id+"/resume", r.admin, "", ""); code != 200 || got["id"] != id || got["paused_at"] != nil {
		t.Fatalf("resuming one that is not paused: %d %v", code, got)
	}
	for what, call := range map[string][2]string{"another domain's key": {bound, id}, "no such endpoint": {r.admin, "nothing"}} {
		if code, name, _ := r.do("POST", "/webhooks/"+call[1]+"/resume", call[0], "", ""); code != 404 || name != "not_found" {
			t.Errorf("resume, %s: %d %s", what, code, name)
		}
	}
	if !strings.Contains(logged.String(), "signing secret was changed") || strings.Contains(logged.String(), "whsec_") ||
		strings.Contains(logged.String(), "app.other.example") {
		t.Fatalf("the log does not say it, or holds the secret or the URL: %s", logged.String())
	}
}
