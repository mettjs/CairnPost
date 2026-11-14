package console_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/console"
	"github.com/mettjs/cairnpost/internal/stats"
	"github.com/mettjs/cairnpost/internal/store"
)

func healthy() bool { return true }

// busy sends sixty emails from one domain, four of which bounce, and
// three from another, one of which does.
func (r *rig) busy() {
	r.t.Helper()
	for i := range 60 {
		m := r.sent("app@high.example.com", fmt.Sprintf("to-%d@example.org", i), "s")
		kind := store.EventDelivered
		if i < 4 {
			kind = store.EventBounced
		}
		r.heard(m, kind, "5.1.1", "")
	}
	for i := range 3 {
		m := r.sent("app@few.example.com", fmt.Sprintf("few-%d@example.org", i), "s")
		if i == 0 {
			r.heard(m, store.EventBounced, "5.1.1", "")
		}
	}
}

// The first page says how much was sent in the period asked for and what
// became of it, marks a domain whose bounce rate is over the line once it
// sent enough for that to be a rate, and names no address.
func TestTheOverviewCountsWhatWasSent(t *testing.T) {
	r := newRig(t, func(d *console.Deps) { d.Events = healthy })
	r.busy()
	b := r.signedIn(r.admin)
	page := b.do("GET", "/", nil).Body.String()
	for _, want := range []string{`<span class="value">63</span>`, `aria-current="true">7 days</a>`, "5 were refused",
		"<code>high.example.com</code>", "Above 5%", "<code>few.example.com</code>", "<b>33.3%</b> 1 of 3", "<b>6.7%</b> 4 of 60",
		`class="chart"`, `class="bar"`, "Show the numbers as a table", `<polyline class="line s2"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
	if strings.Count(page, "Above 5%") != 1 {
		t.Error("three emails with one bounce are not a rate to warn about")
	}
	if strings.Contains(page, "@example.org") || strings.Contains(page, "style=") {
		t.Error("the overview names an address, or styles inline what the content policy forbids")
	}
	if at := order(page, "high.example.com", "few.example.com"); at[0] > at[1] {
		t.Error("the domain that is marked is not first")
	}
	// The period is one of a closed set; anything else is the default.
	if day := b.do("GET", "/?period=24h", nil).Body.String(); !strings.Contains(day, `aria-current="true">24 hours</a>`) ||
		!strings.Contains(day, `<span class="value">63</span>`) {
		t.Error("the last 24 hours are not shown when asked for")
	}
	if odd := b.do("GET", "/?period=%3Cscript%3E", nil).Body.String(); !strings.Contains(odd, `aria-current="true">7 days</a>`) ||
		strings.Contains(odd, "<script>") {
		t.Error("a period that is none was not read as the default")
	}
	// A key bound to a sending domain sees that domain's numbers alone.
	bound, _, err := r.ks.Create(context.Background(), "few's admin", store.ScopeAdmin, "few.example.com")
	if err != nil {
		t.Fatal(err)
	}
	own := r.signedIn(bound).do("GET", "/", nil).Body.String()
	if !strings.Contains(own, `<span class="value">3</span>`) || strings.Contains(own, "high.example.com") {
		t.Errorf("a bound key's overview: %s", own)
	}
}

// noCounts is a store of counts that cannot be read.
type noCounts struct{}

func (noCounts) SendCounts(context.Context, string, int) ([]store.Counted, time.Time, error) {
	return nil, time.Time{}, errors.New("down")
}

// What is not known is said to be not known, never shown as nothing.
func TestTheOverviewSaysWhatIsNotKnown(t *testing.T) {
	// Delivery events are not read: sent is known, outcomes are not.
	r := newRig(t, nil)
	r.busy()
	page := r.signedIn(r.admin).do("GET", "/", nil).Body.String()
	if !strings.Contains(page, `<span class="value">63</span>`) || strings.Count(page, "Delivery events are not read") != 3 || strings.Count(page, `<span class="value none">Not known</span>`) != 3 ||
		strings.Contains(page, "Bounce rate by domain") || strings.Contains(page, "0.0%") {
		t.Errorf("with no events read: %s", page)
	}
	// Nothing was sent: no chart of nothing, and no rate of nothing.
	r = newRig(t, func(d *console.Deps) { d.Events = healthy })
	page = r.signedIn(r.admin).do("GET", "/?period=30d", nil).Body.String()
	if !strings.Contains(page, "Nothing was sent in the last 30 days") || strings.Contains(page, `class="chart"`) ||
		strings.Contains(page, "NaN") || strings.Contains(page, "%</span>") {
		t.Errorf("with nothing sent: %s", page)
	}
	// The counts cannot be read: the page says so and still shows the rest.
	r = newRig(t, func(d *console.Deps) { d.Stats = stats.New(noCounts{}, 5) })
	rec := r.signedIn(r.admin).do("GET", "/", nil)
	page = rec.Body.String()
	if rec.Code != 200 || !strings.Contains(page, "The numbers could not be read") || !strings.Contains(page, "This server") ||
		!strings.Contains(page, "Webhooks") {
		t.Errorf("with the counts unreadable: %d %s", rec.Code, page)
	}
}

// Each way the mail provider's allowance can stand is said, and what the
// provider answered when it failed is never logged.
func TestTheOverviewShowsTheAllowance(t *testing.T) {
	var r *rig
	with := func(q console.Quota, err error) string {
		t.Helper()
		r = newRig(t, func(d *console.Deps) {
			d.Quota = func(context.Context) (console.Quota, error) { return q, err }
		})
		return r.signedIn(r.admin).do("GET", "/", nil).Body.String()
	}
	resets := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	page := with(console.Quota{Limit: 200, Used: 109, Per: "day", ResetsAt: resets}, nil)
	for _, want := range []string{"<b>109</b> of 200 used", "91 left of the 200 the mail provider sends a day.",
		"It starts again at 2 Jan 2030, 00:00 UTC.", `<rect class="fill" width="54.5"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the allowance lacks %q", want)
		}
	}
	if page = with(console.Quota{Limit: 200, Used: 200, Over: true}, nil); !strings.Contains(page, "The allowance is spent.") ||
		!strings.Contains(page, `class="track high wide"`) {
		t.Errorf("a spent allowance: %s", page)
	}
	if page = with(console.Quota{}, errors.New("the provider's own words")); !strings.Contains(page, "did not say how much it will still send") ||
		strings.Contains(page, "the provider's own words") {
		t.Errorf("an allowance that could not be read: %s", page)
	}
	if log := r.logged.String(); strings.Contains(log, "the provider's own words") || !strings.Contains(log, "allowance could not be read") {
		t.Errorf("the log of a failed read: %s", log)
	}
	r = newRig(t, nil)
	if page = r.signedIn(r.admin).do("GET", "/", nil).Body.String(); !strings.Contains(page, "This mail provider has no allowance to tell.") {
		t.Errorf("a provider with no allowance: %s", page)
	}
}

// Emails the provider did not take are said on the first page: a server
// that sends nothing because everything is refused is not an idle one.
func TestTheOverviewSaysWhatWasNotSent(t *testing.T) {
	r := newRig(t, func(d *console.Deps) { d.Events = healthy })
	ctx := context.Background()
	for i := range 3 {
		c, err := r.s.Start(ctx, store.Start{KeyID: r.sendKey.ID, Sender: "app@example.com", Recipient: fmt.Sprintf("no-%d@example.org", i),
			Driver: "fake", TTL: time.Hour, InDoubt: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := r.s.Failed(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	page := r.signedIn(r.admin).do("GET", "/", nil).Body.String()
	for _, want := range []string{"2 not sent.", `/console/emails?state=failed">See them</a>`, "1 with no answer from the mail provider",
		`<span class="value">0</span>`, `<td data-label="Not sent">2</td>`, `class="chart"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "Nothing was sent in the last") {
		t.Error("emails that were refused read as an idle server")
	}
}

// An endpoint that is failing or paused is on the first page, with the
// way to resume it; one that is fine is counted, not listed. A part of
// the server that is not working is said on every page.
func TestTheOverviewLeadsToWhatNeedsALook(t *testing.T) {
	down := false
	r := newRig(t, func(d *console.Deps) {
		d.PauseAfter, d.Events, d.Pruning = 48*time.Hour, healthy, healthy
		d.Sending = func() bool { return !down }
	})
	ctx := context.Background()
	b, l := r.signedIn(r.admin), listen(t)
	if page := b.do("GET", "/", nil).Body.String(); !strings.Contains(page, "No application is told what becomes of emails yet") ||
		!strings.Contains(page, "Everything is working") || strings.Contains(page, `id="server" open`) {
		t.Fatalf("with no endpoint: %s", page)
	}
	id := r.made(b, url.Values{"url": {l.URL + "/hook"}})
	r.made(b, url.Values{"url": {l.URL + "/fine"}, "domain": {"other.example.com"}})
	if page := b.do("GET", "/", nil).Body.String(); !strings.Contains(page, "Nothing needs a look.") || !strings.Contains(page, "2 endpoints are on and answering.") {
		t.Fatalf("with endpoints that are fine: %s", page)
	}
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
	page := b.do("GET", "/", nil).Body.String()
	if !strings.Contains(page, "Failing</span>") || !strings.Contains(page, "Every try has failed since 1 Jan 2030, 00:00 UTC. It is paused if this goes on.") ||
		!strings.Contains(page, "1 endpoint is on and answering.") || strings.Contains(page, ">Resume<") {
		t.Fatalf("a failing endpoint: %s", page)
	}
	r.now = r.now.Add(49 * time.Hour)
	miss(true)
	b = r.signedIn(r.admin)
	page = b.do("GET", "/", nil).Body.String()
	if !strings.Contains(page, "Paused</span>") || !strings.Contains(page, `action="/console/webhooks/`+id+`/resume"`) ||
		!strings.Contains(page, ">Resume<") {
		t.Fatalf("a paused endpoint: %s", page)
	}
	// An endpoint that is turned off is not one that is fine.
	if page = b.follow("POST", "/webhooks/"+id+"/enabled", url.Values{"enabled": {"off"}}).Body.String(); !strings.Contains(page, "</svg>Off</span>") {
		t.Fatalf("turning it off: %s", page)
	}
	if page = b.do("GET", "/", nil).Body.String(); !strings.Contains(page, "Nothing needs a look.") ||
		!strings.Contains(page, "1 endpoint is on and answering. 1 is turned off.") {
		t.Errorf("with one endpoint off: %s", page)
	}
	// A part that is off by how the server is set up is said, as no fault.
	quiet := newRig(t, nil)
	if page := quiet.signedIn(quiet.admin).do("GET", "/keys", nil).Body.String(); !strings.Contains(page, "Working. Off: Delivery events, Webhooks, Pruning") ||
		strings.Contains(page, "pulse-failing") {
		t.Errorf("parts that are off: %s", page)
	}
	// A part that stopped is said on every page, and opened on the first.
	down = true
	for _, at := range []string{"/", "/emails", "/keys"} {
		if page := b.do("GET", at, nil).Body.String(); !strings.Contains(page, "Not working: Webhooks") {
			t.Errorf("%s does not say that webhooks are not being sent", at)
		}
	}
	if page := b.do("GET", "/", nil).Body.String(); !strings.Contains(page, `id="server" open`) {
		t.Error("the first page does not open what is not working")
	}
}
