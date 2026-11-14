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

// Waiting is how many sent emails have no outcome yet: the mail provider
// took them, and no receiving server has taken or refused them. With
// Delivered and Bounced it makes up Sent.
func (t Tally) Waiting() int { return t.Sent - t.Delivered - t.Bounced }

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
	// Now is when the numbers were read, in the zone they are cut in.
	Now time.Time
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
// zone (nil is UTC), and the last one is the one under way.
//
// Emails are counted by the hour of UTC. A day is the hours that begin
// in it, so in a zone that is not whole hours from UTC a day's edge is
// up to an hour off, and an hour reads as beginning at :30 or :45.
func (s *Service) Report(ctx context.Context, bound string, p Period, zone *time.Location) (Report, error) {
	if zone == nil {
		zone = time.UTC
	}
	// The period and the one before it, with hours to spare for the one
	// under way and for a day the clocks made longer.
	rows, now, err := s.store.SendCounts(ctx, bound, 2*p.Steps*p.Hours+4)
	if err != nil {
		return Report{}, err
	}
	// The store's own time cuts the steps: it stamped the emails.
	now = now.In(zone)
	whole := now.Truncate(time.Hour)
	// starts is where each step of the period before, then of this one,
	// begins, and where this one ends.
	starts := make([]time.Time, 2*p.Steps+1)
	for i := range starts {
		starts[i] = p.back(now, 2*p.Steps-1-i)
	}
	r := Report{Period: p, Points: make([]Point, p.Steps), WarnAt: s.warnAt, Now: now}
	for i := range r.Points {
		r.Points[i].At = starts[p.Steps+i]
	}
	// The same stretch one period earlier ends where this one's last
	// whole hour was then: the hour under way is in neither.
	then := whole.Add(-time.Duration(p.Steps) * time.Hour)
	if p.Daily() {
		then = whole.AddDate(0, 0, -p.Steps)
	}
	byDomain := map[string]Tally{}
	for _, c := range rows {
		// The step an hour is in is the last one that begins at or before it.
		i, _ := slices.BinarySearchFunc(starts, c.Hour, func(start, hour time.Time) int {
			if start.After(hour) {
				return 1
			}
			return -1
		})
		i--
		t := tally(c.In)
		switch {
		case i < 0 || i >= 2*p.Steps:
		case i < p.Steps:
			if c.Hour.Before(then) {
				r.Before = r.Before.plus(t)
			}
		default:
			r.Points[i-p.Steps].Tally = r.Points[i-p.Steps].Tally.plus(t)
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
