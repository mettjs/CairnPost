package stats

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata" // the zones below, wherever the tests run

	"github.com/mettjs/cairnpost/internal/store"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func hour(utc string, n int) store.Counted {
	at, err := time.Parse("2006-01-02 15", utc)
	if err != nil {
		panic(err)
	}
	return store.Counted{Hour: at, Domain: "example.com", In: map[string]int{store.StateSent: n}}
}

// Days are days of the zone, the one the clocks made an hour shorter
// too, and the period before ends at the same hour of the clock.
func TestDaysAreTheZonesAcrossAChangeOfTheClocks(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	c := &counts{now: time.Date(2030, 4, 2, 8, 30, 0, 0, time.UTC), rows: []store.Counted{ // 10:30 there
		hour("2030-03-19 22", 1000), // 19 Mar there: older than the period before
		hour("2030-03-19 23", 1),    // 20 Mar, 00:00: the period before begins
		hour("2030-03-26 08", 2),    // 26 Mar, 09:00: the last hour of the same stretch
		hour("2030-03-26 09", 500),  // 26 Mar, 10:00: after it
		hour("2030-03-26 23", 4),    // 27 Mar, 00:00: this period begins
		hour("2030-03-30 23", 8),    // 31 Mar, 00:00, the short day
		hour("2030-03-31 21", 16),   // 31 Mar, 23:00
		hour("2030-03-31 22", 32),   // 1 Apr, 00:00
		hour("2030-04-02 07", 64),   // today, 09:00: the last whole hour
		hour("2030-04-02 08", 128),  // today, the hour under way
	}}
	r, err := New(c, 0).Report(context.Background(), "", Periods[1], berlin)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, p := range r.Points {
		got = append(got, p.Sent)
	}
	if want := []int{4, 0, 0, 0, 24, 32, 192}; len(got) != 7 || [7]int(got) != [7]int(want) {
		t.Fatalf("days hold %v, want %v", got, want)
	}
	if at := r.Points[4].At; at.Format("2 Jan 15:04 MST") != "31 Mar 00:00 CET" || r.Points[5].At.Sub(at) != 23*time.Hour {
		t.Fatalf("the short day begins %s and lasts %s", at, r.Points[5].At.Sub(at))
	}
	if r.Total.Sent != 252 || r.SoFar.Sent != 124 || r.Before.Sent != 3 {
		t.Fatalf("total %d, so far %d, before %d", r.Total.Sent, r.SoFar.Sent, r.Before.Sent)
	}
	if c.hours < 14*24+2 {
		t.Fatalf("%d hours were asked for: too few for a long day", c.hours)
	}
}

// Where the zone is half an hour off, a day is the counted hours that
// begin in it, and an hour reads as it does on the clock there.
func TestAZoneHalfAnHourOff(t *testing.T) {
	kolkata := zone(t, "Asia/Kolkata")
	c := &counts{now: now, rows: []store.Counted{ // 20:00 there
		hour("2030-03-09 18", 1), // 9 Mar, 23:30
		hour("2030-03-09 19", 2), // 10 Mar, 00:30
		hour("2030-03-10 14", 4), // 10 Mar, 19:30: under way
	}}
	r, err := New(c, 0).Report(context.Background(), "", Periods[1], kolkata)
	if err != nil {
		t.Fatal(err)
	}
	if r.Points[5].Sent != 1 || r.Points[6].Sent != 6 || r.Points[6].At.Format("2 Jan 15:04") != "10 Mar 00:00" {
		t.Fatalf("%+v", r.Points[5:])
	}
	if r, err = New(c, 0).Report(context.Background(), "", Periods[0], kolkata); err != nil {
		t.Fatal(err)
	}
	if last := r.Points[23]; last.Sent != 4 || last.At.Format("15:04") != "19:30" || r.Points[4].Sent != 2 || r.Points[3].Sent != 1 {
		t.Fatalf("%+v", r.Points)
	}
}

// Where the clocks go forward at midnight a day has no 00:00: it begins
// at its first hour, under its own date, and holds what was sent in it.
func TestADayWithNoMidnight(t *testing.T) {
	santiago := zone(t, "America/Santiago") // 7 Sep 2025, 00:00 became 01:00
	c := &counts{now: time.Date(2025, 9, 9, 15, 0, 0, 0, time.UTC), rows: []store.Counted{
		hour("2025-09-07 03", 1), // 6 Sep, 23:00
		hour("2025-09-07 04", 2), // 7 Sep, 01:00
	}}
	r, err := New(c, 0).Report(context.Background(), "", Periods[1], santiago)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range r.Points {
		seen[p.At.Format("2 Jan")] = true
	}
	if len(seen) != 7 || r.Points[3].Sent != 1 || r.Points[4].Sent != 2 || r.Points[4].At.Format("2 Jan 15:04") != "7 Sep 01:00" {
		t.Fatalf("%d days: %+v", len(seen), r.Points)
	}
	// The last hour of the day before the change is counted while under way.
	c.now = time.Date(2025, 9, 7, 3, 30, 0, 0, time.UTC)
	if r, err = New(c, 0).Report(context.Background(), "", Periods[1], santiago); err != nil || r.Total.Sent != 1 || r.Points[6].Sent != 1 {
		t.Fatalf("at 23:30 before the change: %+v %v", r.Points, err)
	}
}
