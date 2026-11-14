// Package metrics writes numbers about a running server in the text
// form Prometheus reads. It knows no part of CairnPost: what is counted
// is handed to it. A label never holds an address, a subject, a URL or
// an id: only names from a closed set.
package metrics

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Kinds of metric.
const (
	Counter = "counter"
	Gauge   = "gauge"
)

// Sample is one number: of a metric, with the labels that tell it from
// the metric's other numbers.
type Sample struct {
	// Name is the metric's; Help says what it counts; Kind is Counter
	// or Gauge.
	Name, Help, Kind string
	// Labels are name and value pairs, in the order they are written.
	Labels [][2]string
	Value  float64
}

// Up is 1 for true and 0 for false, as a gauge says yes or no.
func Up(yes bool) float64 {
	if yes {
		return 1
	}
	return 0
}

// Write writes samples as one exposition: each metric's help and kind
// once, then its numbers. Metrics are in order of name, so two
// scrapes of the same state read the same.
func Write(w io.Writer, samples []Sample) error {
	sorted := slices.Clone(samples)
	slices.SortStableFunc(sorted, func(a, b Sample) int { return cmp.Compare(family(a.Name), family(b.Name)) })
	last := ""
	for _, s := range sorted {
		if name := family(s.Name); name != last {
			last = name
			if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, escape(s.Help, false), name, s.Kind); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s%s %s\n", s.Name, labels(s.Labels), strconv.FormatFloat(s.Value, 'g', -1, 64)); err != nil {
			return err
		}
	}
	return nil
}

// family is the metric a sample's name belongs to: a histogram's
// buckets, sum and count are one metric.
func family(name string) string {
	for _, part := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, part); ok && strings.HasSuffix(base, "_seconds") {
			return base
		}
	}
	return name
}

func labels(pairs [][2]string) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p[0] + `="` + escape(p[1], true) + `"`)
	}
	b.WriteByte('}')
	return b.String()
}

// escape writes a help text or a label's value as the format asks.
func escape(s string, value bool) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "\n", `\n`)
	if value {
		s = strings.ReplaceAll(s, `"`, `\"`)
	}
	return s
}
