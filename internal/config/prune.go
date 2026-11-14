package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// MinRetention is the shortest time an email's record may be kept. A
	// receiving server may go on trying an email for days before it
	// bounces, and the record is what its bounce is read against: that the
	// email was sent before a suppression was lifted, and so must not undo
	// the lift.
	MinRetention = 7 * 24 * time.Hour
	// maxRetention is the longest: past it, "off" is what is meant.
	maxRetention = 100 * 365 * 24 * time.Hour
	// MaxContentRetention is the longest an email's words may be kept. The
	// setting is for seeing what was sent while a problem is fresh; kept
	// longer it is an archive of people's mail, which CairnPost is not.
	MaxContentRetention = 30 * 24 * time.Hour
)

// span reads a time in days ("30d") or as a duration ("720h").
func span(v string) (time.Duration, bool) {
	if days, isDays := strings.CutSuffix(v, "d"); isDays {
		// Checked before it is multiplied: a number too large would wrap.
		n, err := strconv.ParseUint(days, 10, 16)
		return time.Duration(n) * 24 * time.Hour, err == nil
	}
	d, err := time.ParseDuration(v)
	return d, err == nil
}

// contentRetention reads CAIRNPOST_CONTENT_RETENTION: how long what an
// email said is kept, or "off", the default, which is 0 and keeps none. It
// cannot outlast the email's record (records; 0 for ever).
func contentRetention(getenv func(string) string, records time.Duration) (time.Duration, error) {
	const name = "CAIRNPOST_CONTENT_RETENTION"
	v := strings.ToLower(strings.TrimSpace(getenv(name)))
	if v == "" || v == "off" {
		return 0, nil
	}
	keep, ok := span(v)
	switch {
	case !ok || keep <= 0:
		return 0, fmt.Errorf("config: %s must be a time such as 24h or 7d, or off to keep nothing an email said (got %q)", name, v)
	case keep > MaxContentRetention:
		return 0, fmt.Errorf("config: %s must be at most 30d (got %q): it is for seeing what was sent while a problem is fresh", name, v)
	case records != 0 && keep > records:
		return 0, fmt.Errorf("config: %s (%s) must not be longer than CAIRNPOST_RETENTION (%s): "+
			"what an email said goes with its record", name, keep, records)
	}
	return keep, nil
}

// retention reads CAIRNPOST_RETENTION: a time in days ("30d") or as a
// duration ("720h"), or "off" to keep records for ever, which is 0. It
// must outlast an Idempotency-Key's life (ttl) and the whole of a
// webhook's retries.
func retention(getenv func(string) string, ttl, retries time.Duration) (time.Duration, error) {
	const name = "CAIRNPOST_RETENTION"
	v := strings.ToLower(strings.TrimSpace(getenv(name)))
	if v == "off" {
		return 0, nil
	}
	keep, ok := 30*24*time.Hour, true
	if v != "" {
		keep, ok = span(v)
	}
	switch {
	case !ok || keep <= 0 || keep > maxRetention:
		return 0, fmt.Errorf("config: %s must be a time such as 30d or 720h, 100 years at most, "+
			"or off to keep records for ever (got %q)", name, v)
	case keep < MinRetention:
		return 0, fmt.Errorf("config: %s must be at least 7d (got %q): a bounce can come days after its email, "+
			"and is read against the email's record", name, v)
	case keep <= ttl:
		return 0, fmt.Errorf("config: %s (%s) must be longer than CAIRNPOST_IDEMPOTENCY_TTL (%s), "+
			"or an email's record would be due while its key is still remembered", name, keep, ttl)
	case keep <= retries:
		return 0, fmt.Errorf("config: %s (%s) must be longer than the waits of CAIRNPOST_WEBHOOK_RETRIES together (%s), "+
			"or a webhook would be deleted with tries still left", name, keep, retries)
	}
	return keep, nil
}

// MinCountsRetention is the shortest time the send counts may be kept:
// the longest period shown (thirty days) beside the one before it, with
// room for the day under way.
const MinCountsRetention = 62 * 24 * time.Hour

// countsRetention reads CAIRNPOST_COUNTS_RETENTION: how long the counts
// of what was sent are kept, in days ("62d") or as a duration, or "off"
// to keep them for ever, which is 0. It is a time of its own, not the
// records': a count outlives the emails it counts.
func countsRetention(getenv func(string) string) (time.Duration, error) {
	const name = "CAIRNPOST_COUNTS_RETENTION"
	v := strings.ToLower(strings.TrimSpace(getenv(name)))
	if v == "off" {
		return 0, nil
	}
	keep, ok := MinCountsRetention, true
	if v != "" {
		keep, ok = span(v)
	}
	switch {
	case !ok || keep <= 0 || keep > maxRetention:
		return 0, fmt.Errorf("config: %s must be a time such as 62d or 2160h, 100 years at most, "+
			"or off to keep the counts for ever (got %q)", name, v)
	case keep < MinCountsRetention:
		return 0, fmt.Errorf("config: %s must be at least 62d (got %q): the last thirty days are shown "+
			"beside the thirty before them", name, v)
	}
	return keep, nil
}
