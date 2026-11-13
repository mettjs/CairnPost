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
)

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
	if days, isDays := strings.CutSuffix(v, "d"); isDays {
		// Checked before it is multiplied: a number too large would wrap.
		n, err := strconv.ParseUint(days, 10, 16)
		keep, ok = time.Duration(n)*24*time.Hour, err == nil
	} else if v != "" {
		d, err := time.ParseDuration(v)
		keep, ok = d, err == nil
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
