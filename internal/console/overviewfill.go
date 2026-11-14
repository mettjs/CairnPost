package console

import (
	"fmt"
	"time"

	"github.com/mettjs/cairnpost/internal/stats"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// fill sets what the page shows of a period's numbers.
func (v *overview) fill(r stats.Report) {
	t := r.Total
	v.Quiet = t.Tried() == 0
	if t.Failed > 0 {
		v.Failed = count(t.Failed)
	}
	if t.Unanswered > 0 {
		v.Unanswered = count(t.Unanswered)
	}
	sent := figure{Key: "sent", Label: "Sent", Value: count(t.Sent), Sub: "in the last " + r.Period.Name}
	if change, known := r.Change(); known && change != 0 {
		sent.Trend, sent.Sub = "up", fmt.Sprintf("%.1f%% more than the %s before", change, r.Period.Name)
		if change < 0 {
			sent.Trend, sent.Sub = "down", fmt.Sprintf("%.1f%% fewer than the %s before", -change, r.Period.Name)
		}
	}
	v.Figures = []figure{sent,
		v.outcome("delivered", "Delivered", t, t.Delivered, "%.1f%%", "arrived"),
		v.outcome("bounced", "Bounced", t, t.Bounced, "%.1f%%", "were refused"),
		v.outcome("complained", "Marked as spam", t, t.Complained, "%.2f%%", "were marked as spam")}
	v.Sent, v.Bad = sentChart(r, 820), outcomeChart(r, 640)
	for i := len(r.Points) - 1; i >= 0; i-- {
		v.Steps = append(v.Steps, stepRow{stepName(r.Points[i].At, r.Period.Daily(), true), r.Points[i].Tally})
	}
	// The bars share one scale, wide enough that the warning line is on it.
	top := max(8, r.WarnAt)
	for _, d := range r.Domains {
		top = max(top, d.Share(d.Bounced))
	}
	for i, d := range r.Domains {
		if i == shown {
			v.MoreDomains = len(r.Domains) - shown
			break
		}
		rate := d.Share(d.Bounced)
		v.Domains = append(v.Domains, domainRow{Name: d.Name, Rate: fmt.Sprintf("%.1f%%", rate), Bounced: count(d.Bounced),
			Sent: count(d.Sent), Fill: round(100 * rate / top), High: d.High})
	}
	if r.WarnAt > 0 {
		v.WarnAt = fmt.Sprintf("%g%%", r.WarnAt)
	}
}

// outcome is a figure for what became of the emails sent: a share of
// them; nothing where none went; not known where delivery events are
// not read.
func (v *overview) outcome(key, label string, t stats.Tally, n int, format, did string) figure {
	switch {
	case !v.Outcomes:
		return figure{Key: key, Label: label, Value: "Not known", Unknown: true, Sub: "Delivery events are not read"}
	case t.Sent == 0:
		return figure{Key: key, Label: label, Value: "0", Sub: "Nothing was sent"}
	}
	return figure{Key: key, Label: label, Value: fmt.Sprintf(format, t.Share(n)), Sub: count(n) + " " + did}
}

// attention picks the endpoints somebody should look at, paused ones
// first, and counts the others: those on and answering, and those off.
func attention(ends []store.Endpoint, pauseAfter time.Duration) (look []hookRow, fine, off int) {
	var failing []hookRow
	for _, e := range ends {
		state := webhook.StateOf(e)
		switch {
		case state == webhook.StatePaused:
			look = append(look, hookRow{Endpoint: e, Label: endpointStates[state][0], Tone: endpointStates[state][1], Paused: true,
				Says: "It failed every try for too long. Nothing is sent to it; what it is owed is kept."})
		case state == webhook.StateOff:
			off++
		case e.FailingSince != nil:
			says := "Every try has failed since " + when(*e.FailingSince) + "."
			if pauseAfter > 0 {
				says += " It is paused if this goes on."
			}
			failing = append(failing, hookRow{Endpoint: e, Label: "Failing", Tone: "failing", Says: says})
		default:
			fine++
		}
	}
	return append(look, failing...), fine, off
}
