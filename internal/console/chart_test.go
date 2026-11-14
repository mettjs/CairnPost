package console

import (
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/stats"
)

// The upright axis ends at a round value at or over the highest one, in
// a few round steps.
func TestAChartsAxisIsRound(t *testing.T) {
	for peak, want := range map[int][2]int{1: {1, 1}, 4: {4, 1}, 7: {8, 2}, 9: {10, 5}, 19: {20, 5}, 87: {100, 25},
		189: {200, 50}, 301: {400, 100}, 1234: {1500, 500}, 180000: {200000, 50000}, 900000: {1000000, 250000}} {
		if top, step := scale(peak); top != want[0] || step != want[1] || top < peak || top/step > 5 {
			t.Errorf("%d: top %d in steps of %d, want %v", peak, top, step, want)
		}
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := count(n); got != want {
			t.Errorf("%d reads %q", n, got)
		}
	}
}

// Every step has a mark, a step with nothing sent has no column, and a
// column never leaves the plot.
func TestAChartHasAMarkForEveryStep(t *testing.T) {
	day := time.Date(2030, 3, 1, 0, 0, 0, 0, time.UTC)
	r := stats.Report{Period: stats.Periods[1]}
	for i, sent := range []int{0, 1, 250, 3, 0, 40, 7} {
		r.Points = append(r.Points, stats.Point{At: day.AddDate(0, 0, i), Tally: stats.Tally{Sent: sent, Bounced: sent / 10}})
	}
	c := sentChart(r, 820)
	if len(c.Marks) != 7 || c.Marks[0].Bar != nil || c.Marks[4].Bar != nil || c.Marks[2].Tip != "3 Mar|250 sent" || len(c.Labels) != 7 {
		t.Fatalf("%+v", c.Marks)
	}
	for i, m := range c.Marks {
		if b := m.Bar; b != nil && (b.Y < c.Top || b.Y+b.H > c.Base+0.1 || b.H < 1.5 || b.X < m.X || b.X+b.W > m.X+m.W+0.1) {
			t.Errorf("column %d leaves its step or the plot: %+v", i, b)
		}
	}
	lines := outcomeChart(r, 640)
	if len(lines.Lines) != 2 || len(lines.Marks) != 7 || len(lines.Marks[2].Dots) != 2 || lines.Marks[2].Tip != "3 Mar|25 bounced|0 marked as spam" {
		t.Fatalf("%+v", lines)
	}
}
