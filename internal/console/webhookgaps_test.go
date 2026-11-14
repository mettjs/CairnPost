package console_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/store"
)

// A signing secret is changed after asking, and the new one is shown on
// the answer to the change, once: asking for the page again asks again.
func TestASigningSecretIsChangedAfterAsking(t *testing.T) {
	r := newRig(t, nil)
	b, l := r.signedIn(r.admin), listen(t)
	id := r.made(b, url.Values{"url": {l.URL + "/hook?token=" + token}})
	at := "/webhooks/" + id
	if page := b.do("GET", at, nil).Body.String(); !strings.Contains(page, at+`/secret">Change the secret</a>`) {
		t.Fatalf("the endpoint's page does not lead to it: %s", page)
	}
	asks := b.do("GET", at+"/secret", nil)
	if page := asks.Body.String(); asks.Code != 200 || !strings.Contains(page, "Change the signing secret?") ||
		!strings.Contains(page, "For 1 day every webhook is signed with both") || strings.Contains(page, "whsec_") {
		t.Fatalf("asking: %d %s", asks.Code, page)
	}
	was, _ := r.s.Endpoint(context.Background(), id)
	rec := b.do("POST", at+"/secret", url.Values{})
	now, _ := r.s.Endpoint(context.Background(), id)
	page := rec.Body.String()
	if rec.Code != 200 || string(now.Secret) == string(was.Secret) || now.PreviousUntil == nil ||
		!strings.Contains(page, "The signing secret was changed") || strings.Count(page, "whsec_") != 1 ||
		!strings.Contains(page, "signed with the old secret as well") {
		t.Fatalf("changing: %d %s", rec.Code, page)
	}
	r.leaks(rec, "after changing the secret")
	if page := b.do("GET", at+"/secret", nil).Body.String(); strings.Contains(page, "whsec_") || !strings.Contains(page, "Change the signing secret?") {
		t.Fatalf("asked for again, the page shows the secret: %s", page)
	}
	if page := b.do("GET", at, nil).Body.String(); !strings.Contains(page, "webhooks are signed with the one before as well") {
		t.Fatalf("the endpoint's page does not say the old one still signs: %s", page)
	}
	// Another domain's key meets no such endpoint, either way.
	bound, _, err := r.ks.Create(context.Background(), "another's admin", store.ScopeAdmin, "other.example")
	if err != nil {
		t.Fatal(err)
	}
	other := r.signedIn(bound)
	for _, method := range []string{"GET", "POST"} {
		if rec := other.do(method, at+"/secret", url.Values{}); rec.Code != 404 {
			t.Errorf("%s by another domain's key: %d", method, rec.Code)
		}
	}
}

// An endpoint that failed for too long is shown as paused, everywhere it
// is shown; it can be resumed or turned off from there, and turning one
// that is off and paused on does not resume it.
func TestAPausedEndpointSaysSoAndIsResumed(t *testing.T) {
	r := newRig(t, func(d *console.Deps) { d.PauseAfter = 48 * time.Hour })
	ctx := context.Background()
	b, l := r.signedIn(r.admin), listen(t)
	id := r.made(b, url.Values{"url": {l.URL + "/hook?token=" + token}})
	at := "/webhooks/" + id
	miss := func(final bool) {
		t.Helper()
		due, err := r.s.TakeDeliveries(ctx, 10, 10, nil, time.Minute)
		if err != nil || len(due) != 1 {
			t.Fatalf("%d taken: %v", len(due), err)
		}
		if _, err := r.s.DeliveryMissed(ctx, due[0].Delivery.ID, due[0].Delivery.Attempts, 503, "status", 0, final, 48*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	r.heard(r.sent("app@example.com", "ana@example.org", "one"), store.EventBounced, "5.1.1", "")
	miss(false)
	if page := b.do("GET", at, nil).Body.String(); !strings.Contains(page, "Every try since") ||
		!strings.Contains(page, "once that has gone on for 2 days, the endpoint is paused.") || !strings.Contains(page, ">Turn off<") ||
		strings.Contains(page, ">Resume<") {
		t.Fatalf("a failing endpoint: %s", page)
	}
	// A test that is answered ends the failing, and the page stops saying it.
	if page := b.follow("POST", at+"/test", url.Values{}).Body.String(); strings.Contains(page, "Every try since") {
		t.Fatalf("after a test was answered the page still says it is failing: %s", page)
	}
	miss(false)
	r.now = r.now.Add(49 * time.Hour)
	miss(true)
	b = r.signedIn(r.admin) // two days on, the sign-in is over
	page := b.do("GET", at, nil).Body.String()
	for _, want := range []string{`</svg>Paused</span>`, "It is still owed everything that happens", ">Resume<", at + `/resume"`,
		">Turn off<", `name="enabled" value="off"`, "Try when resumed"} {
		if !strings.Contains(page, want) {
			t.Errorf("the paused endpoint's page lacks %q", want)
		}
	}
	if strings.Contains(page, "Every try since") {
		t.Error("a paused endpoint is also said to be failing")
	}
	if list := b.do("GET", "/webhooks", nil).Body.String(); !strings.Contains(list, `</svg>Paused</span></td>`) {
		t.Fatalf("the list: %s", list)
	}
	mine, _ := r.s.ListDeliveries(ctx, id, "", "", 10)
	if page = b.follow("POST", at+"/deliveries/"+mine[0].ID+"/retry", url.Values{}).Body.String(); !strings.Contains(page, "The endpoint is paused, so the webhook is tried when it is resumed.") {
		t.Fatalf("a retry while paused: %s", page)
	}
	// Turned off while paused it is off, and on again it is paused still.
	if page = b.follow("POST", at+"/enabled", url.Values{"enabled": {"off"}}).Body.String(); !strings.Contains(page, `</svg>Off</span>`) ||
		!strings.Contains(page, "It was also paused for failing") || strings.Contains(page, ">Resume<") {
		t.Fatalf("turned off while paused: %s", page)
	}
	if page = b.follow("POST", at+"/enabled", url.Values{"enabled": {"on"}}).Body.String(); !strings.Contains(page, "still paused for failing") ||
		!strings.Contains(page, `</svg>Paused</span>`) {
		t.Fatalf("turned on while paused: %s", page)
	}
	page = b.follow("POST", at+"/resume", url.Values{}).Body.String()
	if e, _ := r.s.Endpoint(ctx, id); e.PausedAt != nil || e.FailingSince != nil || !strings.Contains(page, "The endpoint is resumed.") ||
		!strings.Contains(page, `</svg>On</span>`) || !strings.Contains(page, ">Turn off<") || strings.Contains(page, ">Resume<") {
		t.Fatalf("after resuming: %+v %s", e, page)
	}
	// Resumed again it says nothing changed; its own address asked for
	// is the endpoint's page; another domain's key meets no endpoint.
	if page = b.follow("POST", at+"/resume", url.Values{}).Body.String(); !strings.Contains(page, "The endpoint was not paused.") {
		t.Fatalf("resuming twice: %s", page)
	}
	if rec := b.do("GET", at+"/resume", nil); rec.Code != 303 || rec.Header().Get("Location") != "/console"+at {
		t.Fatalf("GET resume: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	bound, _, err := r.ks.Create(ctx, "another's admin", store.ScopeAdmin, "other.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec := r.signedIn(bound).do("POST", at+"/resume", url.Values{}); rec.Code != 404 {
		t.Fatalf("another domain's key: %d", rec.Code)
	}
}
