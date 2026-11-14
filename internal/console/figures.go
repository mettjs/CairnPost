package console

import (
	"fmt"
	"math"
	"strings"

	"github.com/mettjs/cairnpost/internal/stats"
)

// sentChart is how many emails were sent in each step, as columns.
func sentChart(r stats.Report, width int) chart {
	peak := 0
	for _, p := range r.Points {
		peak = max(peak, p.Sent)
	}
	c, x, y := frame("Emails sent each "+unit(r.Period)+", the last "+r.Period.Name, width, 12, r.Points, peak, r.Period.Daily())
	w := min(barMax, c.Marks[0].W*0.62)
	for i, p := range r.Points {
		c.Marks[i].Tip = stepName(p.At, r.Period.Daily(), true) + "|" + count(p.Sent) + " sent"
		if p.Sent > 0 {
			top := min(y(p.Sent), c.Base-1.5)
			c.Marks[i].Bar = &bar{round(x(i) - w/2), top, round(w), round(c.Base - top)}
		}
	}
	return c
}

// outcomeChart is how many of each step's emails bounced and were
// complained about, as two lines.
func outcomeChart(r stats.Report, width int) chart {
	peak := 0
	for _, p := range r.Points {
		peak = max(peak, p.Bounced, p.Complained)
	}
	c, x, y := frame("Emails that bounced or were complained about, by the "+unit(r.Period)+" they were sent, the last "+r.Period.Name,
		width, 124, r.Points, peak, r.Period.Daily())
	series := []struct {
		class, name string
		of          func(stats.Point) int
	}{{"s2", "bounced", func(p stats.Point) int { return p.Bounced }}, {"s3", "marked as spam", func(p stats.Point) int { return p.Complained }}}
	last := len(r.Points) - 1
	// Two names at the end that would touch are left to the legend.
	apart := math.Abs(y(r.Points[last].Bounced)-y(r.Points[last].Complained)) >= 14
	for _, s := range series {
		var at []string
		for i, p := range r.Points {
			at = append(at, fmt.Sprintf("%g,%g", x(i), y(s.of(p))))
			c.Marks[i].Dots = append(c.Marks[i].Dots, dot{x(i), y(s.of(p)), s.class})
		}
		l := line{Class: s.class, Points: strings.Join(at, " "), EndX: x(last), EndY: y(s.of(r.Points[last]))}
		l.EndLift = 4
		if l.EndY > c.Base-4 {
			l.EndLift = -5
		}
		if apart {
			l.End = count(s.of(r.Points[last])) + " " + s.name
		}
		c.Lines = append(c.Lines, l)
	}
	for i, p := range r.Points {
		c.Marks[i].Tip = stepName(p.At, r.Period.Daily(), true) + "|" + count(p.Bounced) + " bounced|" + count(p.Complained) + " marked as spam"
	}
	return c
}

func unit(p stats.Period) string {
	if p.Daily() {
		return "day"
	}
	return "hour"
}

// count is a number with its thousands set apart.
func count(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
