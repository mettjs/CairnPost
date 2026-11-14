// Package stats says how much was sent and what became of it, over a
// period: the numbers of the console's first page. It reads the send
// counts, which outlive the emails' records, and holds the rules for
// reading them: what counts as sent, how a period is cut, and when a
// sending domain's bounce rate is worth a warning.
package stats

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// MinSent is how many emails a sending domain must have sent in a period
// before its bounce rate can warn: one bounce of three is not a rate.
const MinSent = 50

// Period is a stretch of time the numbers are shown over, cut in steps.
type Period struct {
	// Key names it in a URL; Name is how it reads after "the last".
	Key, Name string
	// Steps is how many steps it has, each Hours long: an hour or a day.
	Steps, Hours int
}

// Periods is every period, shortest first.
var Periods = []Period{{"24h", "24 hours", 24, 1}, {"7d", "7 days", 7, 24}, {"30d", "30 days", 30, 24}}

// Default is the period shown when none is asked for.
var Default = Periods[1]

// PeriodOf is the period a key names, and whether it names one.
func PeriodOf(key string) (Period, bool) {
	for _, p := range Periods {
		if p.Key == key {
			return p, true
		}
	}
	return Default, false
}

// Daily reports whether the period's steps are days.
func (p Period) Daily() bool { return p.Hours == 24 }

// Tally is what became of some emails.
type Tally struct {
	// Sent is how many the mail provider took: not those it refused, nor
	// those whose send is under way or was never answered.
	Sent int
	// Delivered is how many a receiving server took, those complained
	// about after they arrived included.
	Delivered, Bounced, Complained int
	// Failed is how many the mail provider refused or could not send;
	// Unanswered how many have no answer from it: under way, or never
	// answered. Neither is in Sent.
	Failed, Unanswered int
}

// Tried is every email a send was begun for, whatever became of it.
func (t Tally) Tried() int { return t.Sent + t.Failed + t.Unanswered }

func tally(in map[string]int) Tally {
	t := Tally{Delivered: in[store.StateDelivered] + in[store.StateComplained], Bounced: in[store.StateBounced],
		Complained: in[store.StateComplained], Failed: in[store.StateFailed],
		Unanswered: in[store.StateSending] + in[store.StateUnknown]}
	t.Sent = in[store.StateSent] + in[store.StateDeferred] + in[store.StateDelivered] + in[store.StateBounced] + in[store.StateComplained]
	return t
}

func (t Tally) plus(o Tally) Tally {
	return Tally{t.Sent + o.Sent, t.Delivered + o.Delivered, t.Bounced + o.Bounced, t.Complained + o.Complained,
		t.Failed + o.Failed, t.Unanswered + o.Unanswered}
}

// Share is n as a percentage of what was sent, 0 when nothing was.
func (t Tally) Share(n int) float64 {
	if t.Sent == 0 {
		return 0
	}
	return 100 * float64(n) / float64(t.Sent)
}

// Point is one step of a period: the emails sent in it, and what has
// become of them since. An email counts where it was sent, so a bounce
// that came days later is in the step its email was sent in.
type Point struct {
	At time.Time
	Tally
}

// Domain is one sending domain's part of a period.
type Domain struct {
	Name string
	Tally
	// High says its bounce rate is at or over the warning line.
	High bool
}

// Report is a period's numbers.
type Report struct {
	Period Period
	// Points is every step, oldest first, those with nothing sent too.
	Points []Point
	// Total is the period's sum.
	Total Tally
	// SoFar is the period up to its last whole hour, and Before the same
	// stretch one period earlier: like is compared with like, so a step
	// that is under way never reads as a fall.
	SoFar, Before Tally
	// Domains is each sending domain that sent in the period: those
	// marked first, then those that sent enough for their bounce rate
	// to be one, then the rest; within each, the highest rate first.
	Domains []Domain
	// WarnAt is the bounce rate, in percent, a domain warns at; 0 for never.
	WarnAt float64
}

// Change is how much more was sent than in the period before, in percent
// (negative for less), and whether there is anything to compare with.
func (r Report) Change() (float64, bool) {
	if r.Before.Sent == 0 {
		return 0, false
	}
	return 100 * float64(r.SoFar.Sent-r.Before.Sent) / float64(r.Before.Sent), true
}

// Service reads the numbers.
type Service struct {
	store  store.Counts
	warnAt float64
}

// New returns the service. warnAt is the bounce rate, in percent, at
// which a sending domain is marked; 0 marks none.
func New(s store.Counts, warnAt float64) *Service { return &Service{store: s, warnAt: warnAt} }

// Report answers a period's numbers: those of one sending domain for a
// key bound to it, of every domain for "". Steps are hours and days of
// UTC, and the last one is the one under way.
func (s *Service) Report(ctx context.Context, bound string, p Period) (Report, error) {
	// The period and the one before it, with an hour to spare.
	rows, now, err := s.store.SendCounts(ctx, bound, 2*p.Steps*p.Hours+1)
	if err != nil {
		return Report{}, err
	}
	// The store's own time cuts the steps: it stamped the emails.
	step := time.Duration(p.Hours) * time.Hour
	first := now.UTC().Truncate(step).Add(-time.Duration(p.Steps-1) * step)
	r := Report{Period: p, Points: make([]Point, p.Steps), WarnAt: s.warnAt}
	for i := range r.Points {
		r.Points[i].At = first.Add(time.Duration(i) * step)
	}
	// The hour under way is in neither side of the comparison.
	whole, length := now.UTC().Truncate(time.Hour), time.Duration(p.Steps)*step
	byDomain := map[string]Tally{}
	for _, c := range rows {
		t := tally(c.In)
		if !c.Hour.Before(first.Add(-length)) && c.Hour.Before(whole.Add(-length)) {
			r.Before = r.Before.plus(t)
		}
		if i := int(c.Hour.Sub(first) / step); !c.Hour.Before(first) && i < p.Steps {
			r.Points[i].Tally = r.Points[i].Tally.plus(t)
			r.Total = r.Total.plus(t)
			byDomain[c.Domain] = byDomain[c.Domain].plus(t)
			if c.Hour.Before(whole) {
				r.SoFar = r.SoFar.plus(t)
			}
		}
	}
	for name, t := range byDomain {
		if t.Sent == 0 {
			continue
		}
		r.Domains = append(r.Domains, Domain{Name: name, Tally: t,
			High: s.warnAt > 0 && t.Sent >= MinSent && t.Share(t.Bounced) >= s.warnAt})
	}
	slices.SortFunc(r.Domains, func(a, b Domain) int {
		return cmp.Or(-cmp.Compare(rank(a), rank(b)), cmp.Compare(b.Share(b.Bounced), a.Share(a.Bounced)), cmp.Compare(a.Name, b.Name))
	})
	return r, nil
}

// rank orders domains by how much their bounce rate says: a marked one
// most, one that sent too little for a rate least.
func rank(d Domain) int {
	switch {
	case d.High:
		return 2
	case d.Sent >= MinSent:
		return 1
	}
	return 0
}
