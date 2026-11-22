package api

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	waitSeconds = regexp.MustCompile(`^[0-9]{1,9}$`)
	// A value every client's own test server writes as the same bytes.
	plainValue = regexp.MustCompile(`^[!-~](?:[ -~]*[!-~])?$`)
	waitDate   = regexp.MustCompile(`^(Mon|Tue|Wed|Thu|Fri|Sat|Sun), ([0-9]{2}) (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) ([0-9]{4}) ([0-9]{2}):([0-9]{2}):([0-9]{2}) GMT$`)
)

// dateOf is the time a Retry-After date names, when it is the one form
// of a date, a time that exists, and the day of the week it falls on.
func dateOf(given string) (time.Time, bool) {
	m := waitDate.FindStringSubmatch(given)
	if m == nil {
		return time.Time{}, false
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	month := time.Month(1 + strings.Index("JanFebMarAprMayJunJulAugSepOctNovDec", m[3])/3)
	at := time.Date(n(m[4]), month, n(m[2]), n(m[5]), n(m[6]), n(m[7]), 0, time.UTC)
	// A time that does not exist is carried over into one that does.
	exists := at.Day() == n(m[2]) && at.Month() == month && at.Hour() == n(m[5]) && at.Minute() == n(m[6]) && at.Second() == n(m[7])
	return at, exists && at.Weekday().String()[:3] == m[1]
}

type waitCase struct {
	Given   string   `json:"given"`
	Seconds *float64 `json:"seconds"`
}

// readCase is a value read at a time the case says.
type readCase struct {
	Now     *int64   `json:"now"`
	Given   string   `json:"given"`
	Seconds *float64 `json:"seconds"`
}

func waitCases(t *testing.T) (header, batch []waitCase, read []readCase) {
	t.Helper()
	var file struct {
		Header []waitCase `json:"header"`
		Batch  []waitCase `json:"batch"`
		Read   []readCase `json:"read"`
	}
	raw, err := os.ReadFile("../../client-cases/waits.json")
	if err != nil {
		t.Fatal(err)
	}
	// A field no list has is a case that is not looked at whole.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Read {
		if c.Now == nil {
			t.Fatalf("%q is read at no time", c.Given)
		}
	}
	if len(file.Header) < 20 || len(file.Batch) < 10 || len(file.Read) < 20 {
		t.Fatalf("only %d, %d and %d cases were read", len(file.Header), len(file.Batch), len(file.Read))
	}
	return file.Header, file.Batch, file.Read
}

// said is a case's wait as text, for a message.
func said(seconds *float64) string {
	if seconds == nil {
		return "none"
	}
	return strconv.FormatFloat(*seconds, 'f', -1, 64)
}

// Each wait the cases ask for is the one the rule gives: so many seconds
// for 1 to 9 digits of more than nothing, and none for anything else. A
// date in the cases must ask for no wait on any day the cases are run,
// so it is no date at all, or one long gone, or one out of reach.
func TestTheClientCasesReadARetryAfterByTheRule(t *testing.T) {
	header, _, _ := waitCases(t)
	forms := map[string]int{}
	for _, c := range header {
		var want *float64
		if waitSeconds.MatchString(c.Given) {
			if n, _ := strconv.Atoi(c.Given); n > 0 {
				f := float64(n)
				want = &f
			}
			forms["seconds"]++
		} else if at, exists := dateOf(c.Given); exists {
			if at.Year() > 2000 && at.Year() < 9000 {
				t.Errorf("%q is a date that would ask for a wait on some day, and for none on another", c.Given)
			}
			forms["a date"]++
		} else if waitDate.MatchString(c.Given) {
			forms["a date that is none"]++
		}
		if said(c.Seconds) != said(want) {
			t.Errorf("%q: the case says %s, the rule %s", c.Given, said(c.Seconds), said(want))
		}
		if !plainValue.MatchString(c.Given) {
			t.Errorf("%q would not reach every client's test as the same bytes", c.Given)
		}
	}
	for _, form := range []string{"seconds", "a date", "a date that is none"} {
		if forms[form] < 2 {
			t.Errorf("only %d cases of %s", forms[form], form)
		}
	}
}

// Read at a time that is said, a date still to come is a wait of the
// seconds until it, a part of one counted whole; and around a value only
// a space, a tab and a line break are dropped.
func TestTheClientCasesReadAWaitAtASaidTime(t *testing.T) {
	_, _, read := waitCases(t)
	waits, odd := 0, 0
	for _, c := range read {
		var want *float64
		given := strings.Trim(c.Given, " \t\r\n")
		if waitSeconds.MatchString(given) {
			if n, _ := strconv.Atoi(given); n > 0 {
				f := float64(n)
				want = &f
			}
		} else if at, exists := dateOf(given); exists {
			// Whole seconds, rounded up, counted in milliseconds: the
			// cases say the clock no finer.
			left := at.UnixMilli() - *c.Now
			if whole := (left + 999) / 1000; left > 0 && whole <= longestWait {
				f := float64(whole)
				want = &f
				waits++
			}
		}
		if !plainValue.MatchString(c.Given) {
			odd++
		}
		if said(c.Seconds) != said(want) {
			t.Errorf("%q at %d: the case says %s, the rule %s", c.Given, *c.Now, said(c.Seconds), said(want))
		}
	}
	if waits < 5 || odd < 5 {
		t.Errorf("only %d dates still to come, and %d values with something around them", waits, odd)
	}
}

// In a batch's answer a wait is a whole number from one to the longest,
// however it is written, and anything else JSON can say there is none.
func TestTheClientCasesReadABatchsWaitByTheRule(t *testing.T) {
	_, batch, _ := waitCases(t)
	numbers, others := 0, 0
	for _, c := range batch {
		if !json.Valid([]byte(c.Given)) {
			t.Errorf("%q is not JSON, and the answer around it would not be read at all", c.Given)
			continue
		}
		var want *float64
		var n float64
		if err := json.Unmarshal([]byte(c.Given), &n); err == nil && c.Given != "null" {
			numbers++
			if n >= 1 && n <= longestWait && n == float64(int64(n)) {
				want = &n
			}
		} else {
			others++
		}
		if said(c.Seconds) != said(want) {
			t.Errorf("%s: the case says %s, the rule %s", c.Given, said(c.Seconds), said(want))
		}
	}
	if numbers < 5 || others < 5 {
		t.Errorf("only %d numbers and %d of anything else", numbers, others)
	}
}

// What the server writes of a wait is what a client reads as one: whole
// seconds in digits in the header, and the same number in a batch. So
// for a wait of any length.
func TestAWaitTheServerWritesIsOneAClientReads(t *testing.T) {
	for _, d := range []time.Duration{math.MinInt64, 0, time.Nanosecond, 1200 * time.Millisecond, 30 * 24 * time.Hour, 40 * 365 * 24 * time.Hour, math.MaxInt64} {
		wait := seconds(d)
		if n, _ := strconv.Atoi(wait); !waitSeconds.MatchString(wait) || n < 1 {
			t.Errorf("a wait of %v is written %q", d, wait)
		}
	}
	item, err := json.Marshal(itemProblem{problem{StatusCode: 429, Name: "rate_limit_exceeded", Message: "Later."}, 30})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Wait json.RawMessage `json:"retry_after"`
	}
	if err := json.Unmarshal(item, &back); err != nil || !waitSeconds.MatchString(string(back.Wait)) {
		t.Errorf("a batch says a wait as %s (%v)", back.Wait, err)
	}
}
