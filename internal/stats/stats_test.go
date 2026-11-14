package stats

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// counts is a store of counts a test writes by hand.
type counts struct {
	rows []store.Counted
	now  time.Time
	err  error
	// asked is what the last call asked for.
	domain string
	hours  int
}

func (c *counts) SendCounts(_ context.Context, domain string, hours int) ([]store.Counted, time.Time, error) {
	c.domain, c.hours = domain, hours
	var out []store.Counted
	for _, r := range c.rows {
		if domain == "" || r.Domain == domain {
			out = append(out, r)
		}
	}
	return out, c.now, c.err
}

var now = time.Date(2030, 3, 10, 14, 30, 0, 0, time.UTC)

func at(daysAgo, hour int, domain string, in map[string]int) store.Counted {
	day := now.Truncate(24*time.Hour).AddDate(0, 0, -daysAgo)
	return store.Counted{Hour: day.Add(time.Duration(hour) * time.Hour), Domain: domain, In: in}
}

// What counts as sent is what the provider took; a complaint is of an
// email that arrived.
func TestWhatCountsAsSentAndDelivered(t *testing.T) {
	got := tally(map[string]int{store.StateSending: 9, store.StateUnknown: 9, store.StateFailed: 9,
		store.StateSent: 1, store.StateDeferred: 2, store.StateDelivered: 30, store.StateBounced: 4, store.StateComplained: 3})
	if got != (Tally{Sent: 40, Delivered: 33, Bounced: 4, Complained: 3, Failed: 9, Unanswered: 18}) || got.Tried() != 67 || got.Waiting() != 3 {
		t.Fatalf("%+v", got)
	}
	if got.Share(4) != 10 || (Tally{}).Share(0) != 0 {
		t.Fatalf("%v %v", got.Share(4), (Tally{}).Share(0))
	}
}

// Seven days are seven steps of a UTC day, the last one today, every one
// present; what was sent before them is the period before, and no older.
func TestAPeriodIsCutInStepsWithThePeriodBefore(t *testing.T) {
	c := &counts{now: now, rows: []store.Counted{
		at(0, 9, "example.com", map[string]int{store.StateDelivered: 5}),
		at(0, 13, "example.com", map[string]int{store.StateBounced: 1}),
		at(6, 0, "example.com", map[string]int{store.StateSent: 2}),
		at(7, 23, "example.com", map[string]int{store.StateDelivered: 4}),    // the period before
		at(13, 0, "example.com", map[string]int{store.StateDelivered: 6}),    // its first day
		at(14, 23, "example.com", map[string]int{store.StateDelivered: 100}), // older than both
	}}
	r, err := New(c, 5).Report(context.Background(), "", Periods[1], nil)
	if err != nil || len(r.Points) != 7 || c.hours < 14*24 || c.domain != "" {
		t.Fatalf("%d points, %d hours asked, %v", len(r.Points), c.hours, err)
	}
	if !r.Points[6].At.Equal(now.Truncate(24*time.Hour)) || r.Points[6].Sent != 6 || r.Points[6].Bounced != 1 ||
		r.Points[0].Sent != 2 || r.Points[3].Sent != 0 {
		t.Fatalf("%+v", r.Points)
	}
	// The stretch up to the last whole hour is compared with the same
	// stretch a week earlier: what came after it then is left out.
	if r.Total.Sent != 8 || r.SoFar.Sent != 8 || r.Before.Sent != 6 {
		t.Fatalf("total %+v, so far %+v, before %+v", r.Total, r.SoFar, r.Before)
	}
	if change, known := r.Change(); !known || int(change) != 33 {
		t.Fatalf("%v %v", change, known)
	}
	// Hours: the one under way is the last of twenty-four.
	r, err = New(c, 5).Report(context.Background(), "", Periods[0], nil)
	if err != nil || len(r.Points) != 24 || !r.Points[23].At.Equal(now.Truncate(time.Hour)) ||
		r.Points[22].Bounced != 1 || r.Points[18].Delivered != 5 || r.Total.Sent != 6 {
		t.Fatalf("%+v %v", r.Points, err)
	}
	if _, known := (Report{}).Change(); known {
		t.Fatal("nothing before is nothing to compare with")
	}
}

// A domain's bounce rate warns at the line, once enough was sent for it
// to be a rate; a bound key is given its own domain's numbers only.
func TestADomainWarnsAtTheLineOnceEnoughWasSent(t *testing.T) {
	c := &counts{now: now, rows: []store.Counted{
		at(1, 1, "high.example.com", map[string]int{store.StateDelivered: 95, store.StateBounced: 5}),
		at(1, 1, "few.example.com", map[string]int{store.StateDelivered: 2, store.StateBounced: 1}),
		at(1, 1, "fine.example.com", map[string]int{store.StateDelivered: 196, store.StateBounced: 4}),
		at(1, 1, "idle.example.com", map[string]int{store.StateFailed: 3}),
	}}
	r, err := New(c, 5).Report(context.Background(), "", Periods[1], nil)
	if err != nil || len(r.Domains) != 3 {
		t.Fatalf("%+v %v", r.Domains, err)
	}
	// The marked one first, then the one whose rate is a rate, then the
	// one that sent too little, however high its share.
	want := []Domain{{"high.example.com", Tally{Sent: 100, Delivered: 95, Bounced: 5}, true},
		{"fine.example.com", Tally{Sent: 200, Delivered: 196, Bounced: 4}, false},
		{"few.example.com", Tally{Sent: 3, Delivered: 2, Bounced: 1}, false}}
	for i, d := range r.Domains {
		if d != want[i] {
			t.Errorf("%d: %+v, want %+v", i, d, want[i])
		}
	}
	if r, _ = New(c, 0).Report(context.Background(), "", Periods[1], nil); r.Domains[0].High || r.WarnAt != 0 {
		t.Fatal("a warning that is off still warned")
	}
	r, err = New(c, 5).Report(context.Background(), "fine.example.com", Periods[2], nil)
	if err != nil || c.domain != "fine.example.com" || len(r.Domains) != 1 || r.Total.Sent != 200 {
		t.Fatalf("%+v asked of %q: %v", r.Domains, c.domain, err)
	}
	c.err = errors.New("down")
	if _, err := New(c, 5).Report(context.Background(), "", Periods[1], nil); err == nil {
		t.Fatal("a store that failed was read as nothing sent")
	}
}

func TestAPeriodIsOneOfAClosedSet(t *testing.T) {
	for _, p := range Periods {
		if got, ok := PeriodOf(p.Key); !ok || got != p {
			t.Errorf("%q: %+v %v", p.Key, got, ok)
		}
	}
	if got, ok := PeriodOf("90d"); ok || got != Default {
		t.Fatalf("%+v %v", got, ok)
	}
}

// Steady sending never reads as a fall because the day is not over: the
// comparison stops at the last whole hour on both sides.
func TestSteadySendingIsNoChange(t *testing.T) {
	c := &counts{now: time.Date(2030, 3, 10, 0, 5, 0, 0, time.UTC)}
	for h := c.now.Truncate(time.Hour); h.After(c.now.AddDate(0, 0, -61)); h = h.Add(-time.Hour) {
		c.rows = append(c.rows, store.Counted{Hour: h, Domain: "example.com", In: map[string]int{store.StateDelivered: 40}})
	}
	for _, p := range Periods {
		r, err := New(c, 0).Report(context.Background(), "", p, nil)
		if change, known := r.Change(); err != nil || !known || change != 0 {
			t.Errorf("%s: %v %v %v", p.Key, change, known, err)
		}
	}
}
