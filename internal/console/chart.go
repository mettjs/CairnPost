package console

import (
	"math"
	"time"

	"github.com/mettjs/cairnpost/internal/stats"
)

// A chart is drawn by the server: these are the numbers its template
// writes as SVG, so a page needs no script to show one.
type chart struct {
	// Title says what the chart shows, for who cannot see it.
	Title string
	W, H  int
	// Left, Right, Top and Base bound the plot; Height is from Top to Base.
	Left, Right, Top, Base, Height float64
	Ticks                          []tick
	Labels                         []label
	Lines                          []line
	Marks                          []mark
}

// tick is a value on the upright axis; label a step named under the plot.
type tick struct {
	Y     float64
	Label string
}

type label struct {
	X    float64
	Text string
}

// line is one series drawn through every step, and named at its end.
type line struct {
	// Class is the series' colour class.
	Class, Points string
	EndX, EndY    float64
	// EndLift moves the name off the axis where the last value is on it.
	EndLift float64
	// End names the last value beside it, "" where two would collide.
	End string
}

// mark is one step: what the pointer finds there, and what it is told.
type mark struct {
	// X and W are the step's whole band; Mid its middle.
	X, W, Mid float64
	Tip       string
	// Bar is the step's column, nil for a step with none.
	Bar  *bar
	Dots []dot
}

type bar struct{ X, Y, W, H float64 }

type dot struct {
	X, Y  float64
	Class string
}

const (
	chartH   = 220
	chartTop = 12.0
	chartPad = 26.0 // under the plot, for the steps' names
	axisPad  = 40.0 // left of the plot, for the values
	barMax   = 24.0
)

// round keeps a coordinate short in the page.
func round(v float64) float64 { return math.Round(v*10) / 10 }

// scale is a top value at or above peak that divides into a few round
// steps, and the step.
func scale(peak int) (top, step int) {
	for mag := 1; ; mag *= 10 {
		for _, s := range []int{mag, 2 * mag, 25 * mag / 10, 5 * mag} {
			if peak <= s*4 {
				return s * max(1, (peak+s-1)/s), s
			}
		}
	}
}

// frame lays out a plot of some steps under a peak value, with room on
// the right for what is written at a line's end.
func frame(title string, width int, right float64, points []stats.Point, peak int, daily bool) (chart, func(int) float64, func(int) float64) {
	top, step := scale(max(peak, 1))
	c := chart{Title: title, W: width, H: chartH, Left: axisPad, Right: float64(width) - right, Top: chartTop, Base: chartH - chartPad,
		Height: chartH - chartPad - chartTop}
	y := func(v int) float64 { return round(c.Base - (c.Base-c.Top)*float64(v)/float64(top)) }
	band := (c.Right - c.Left) / float64(len(points))
	x := func(i int) float64 { return round(c.Left + band*(float64(i)+0.5)) }
	for v := 0; v <= top; v += step {
		c.Ticks = append(c.Ticks, tick{y(v), count(v)})
	}
	every := 1
	switch {
	case len(points) > 24:
		every = 5
	case len(points) > 8:
		every = 3
	}
	for i, p := range points {
		if (len(points)-1-i)%every == 0 {
			c.Labels = append(c.Labels, label{x(i), stepName(p.At, daily, false)})
		}
		c.Marks = append(c.Marks, mark{X: round(c.Left + band*float64(i)), W: round(band), Mid: x(i)})
	}
	return c, x, y
}

// stepName is a step as a person reads it: a day, or an hour of one.
func stepName(at time.Time, daily, full bool) string {
	switch {
	case daily:
		return at.Format("2 Jan")
	case full:
		return at.Format("2 Jan, 15:04") + " UTC"
	}
	return at.Format("15:04")
}
