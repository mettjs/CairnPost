package console

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mettjs/cairnpost/internal/stats"
	"github.com/mettjs/cairnpost/internal/store"
)

// overview is the first page: what was sent over a period and what became
// of it, what needs a look, and whether each part of the server is working.
type overview struct {
	Period  stats.Period
	Periods []stats.Period
	// Unread says the numbers could not be read: the rest is still shown.
	Unread bool
	// Outcomes says delivery events are read, so what became of an email
	// is known; without them only "sent" is.
	Outcomes bool
	// Quiet says no send was begun in the period.
	Quiet   bool
	Figures []figure
	// Failed and Unanswered are how many emails the provider did not
	// take, and how many it gave no answer about: "" for none.
	Failed, Unanswered string
	// Waiting is how many sent emails have no outcome yet, "" for none
	// or where outcomes are not read: with it the shares make a whole.
	Waiting string
	Sent    chart
	Bad     chart
	Steps   []stepRow
	// Zone names the time zone the hours and days are those of.
	Zone    string
	Domains []domainRow
	// MoreDomains is how many sending domains are not listed: the page
	// shows the few whose rate says most.
	MoreDomains int
	// WarnAt is the bounce rate a domain is marked at, "" for none.
	WarnAt string
	Quota  *quotaView
	// Hooks are the endpoints that need a look, the first few of them;
	// MoreHooks is how many more do, Fine how many are on and answering,
	// and Off how many are turned off.
	Hooks       []hookRow
	MoreHooks   int
	Fine, Off   int
	HooksUnread bool
	Parts       []part
	Driver      string
}

// shown is how many rows a list on the first page shows: it leads to
// the pages that hold the whole of each.
const shown = 6

// figure is one of the numbers the page leads with.
type figure struct {
	Key, Label, Value, Sub string
	// Unknown says the value is not known, and Value says so.
	Unknown bool
	// Trend is "up" or "down" where Sub compares with the period before.
	Trend string
}

// stepRow is one step of the period, for the table under the chart.
type stepRow struct {
	Name string
	stats.Tally
}

type domainRow struct {
	Name, Rate    string
	Bounced, Sent string
	// Fill is how much of its bar is filled, of 100; High marks a rate
	// at or over the warning line. Wide is the bar's other size, not
	// this one's.
	Fill       float64
	High, Wide bool
}

// hookRow is an endpoint that needs a look: how it stands, as every page
// names and colours it, and why.
type hookRow struct {
	store.Endpoint
	Label, Tone, Says string
	// Owed is how many webhooks it is owed: those waiting, and for a
	// paused one those given up while it failed. "" for none, or not known.
	Owed string
	// Paused says it can be resumed.
	Paused bool
}

// overviewPage is GET /: the numbers of the period asked for. The mail
// provider is asked for its allowance beside the reads of the database,
// so a slow answer holds the page up no longer than it must.
func (c *console) overviewPage(w http.ResponseWriter, r *http.Request) {
	key := r.Context().Value(whoKey{}).(store.Key)
	period, _ := stats.PeriodOf(r.URL.Query().Get("period"))
	v := overview{Period: period, Periods: stats.Periods, Outcomes: c.Events != nil, Driver: c.Driver, Parts: c.parts()}
	allowance := make(chan *quotaView, 1)
	zone := c.zoneOf(r)
	go func() {
		defer c.caught(func() { allowance <- &quotaView{Unread: true} })
		allowance <- c.quota(r.Context(), zone)
	}()
	hooks := make(chan struct{})
	go func() {
		defer close(hooks)
		defer c.caught(func() { v.Hooks, v.HooksUnread = nil, true })
		c.hooks(r.Context(), key.Domain, zone, &v)
	}()
	report, err := c.Stats.Report(r.Context(), key.Domain, period, zone)
	<-hooks // it wrote to the page: nothing else does until it is done
	if err != nil {
		c.Log.Error("console: the send counts could not be read", "error", err)
		v.Unread = true
	} else {
		v.fill(report)
	}
	v.Quota = <-allowance
	c.render(w, r, http.StatusOK, "overview", v)
}

// hooks sets the endpoints that need a look and what each is owed.
func (c *console) hooks(ctx context.Context, domain string, zone *time.Location, v *overview) {
	ends, err := c.Webhooks.List(ctx, domain)
	if err != nil {
		c.Log.Error("console: the endpoints could not be read", "error", err)
		v.HooksUnread = true
		return
	}
	v.Hooks, v.Fine, v.Off = attention(ends, c.PauseAfter, zone)
	if len(v.Hooks) > shown {
		v.Hooks, v.MoreHooks = v.Hooks[:shown], len(v.Hooks)-shown
	}
	c.owed(ctx, v.Hooks)
}

// caught keeps a panic in a read made beside the page's own from ending
// the server: it is logged, and then says what the page shows instead.
func (c *console) caught(then func()) {
	if p := recover(); p != nil {
		c.Log.Error("console: a read for the first page panicked", "panic", fmt.Sprint(p))
		then()
	}
}
