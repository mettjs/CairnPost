package config

import (
	"fmt"
	"time"
)

// providerWait reads CAIRNPOST_MAX_PROVIDER_WAIT: the longest the server
// holds back because the mail provider asked it to wait. Default 1h.
//
// The provider's own word is not a bound: one wrong answer asking for
// years would stop scheduled sending for years. An hour is the longest
// the server waits between two tries of its own, and well inside the
// day an email is still tried after its time unless
// CAIRNPOST_SCHEDULE_GIVE_UP says otherwise: an email put off when its
// time comes is tried many times more. One put off past the end of that
// time has one last try, as after any wait.
func providerWait(getenv func(string) string) (time.Duration, error) {
	wait, err := duration(getenv, "CAIRNPOST_MAX_PROVIDER_WAIT", time.Hour)
	if err != nil {
		return 0, err
	}
	if wait < time.Minute || wait > 7*24*time.Hour {
		return 0, fmt.Errorf("config: CAIRNPOST_MAX_PROVIDER_WAIT must be between 1m and 168h (got %s)", wait)
	}
	return wait, nil
}
