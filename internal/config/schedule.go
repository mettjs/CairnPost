package config

import (
	"fmt"
	"strings"
	"time"
)

// Schedule is how emails asked for ahead of time are kept and sent.
type Schedule struct {
	// Ahead is CAIRNPOST_SCHEDULE_AHEAD: how far ahead an email may be
	// asked for. Default 30d.
	Ahead time.Duration
	// MaxBytes is CAIRNPOST_SCHEDULE_MAX_BYTES: how much the emails
	// that wait may hold in all, words and files. Default 1 GiB.
	MaxBytes int64
	// Poll is CAIRNPOST_SCHEDULE_POLL: how often emails whose time has
	// come are looked for. Default 5s.
	Poll time.Duration
	// GiveUp is CAIRNPOST_SCHEDULE_GIVE_UP: how long after its time an
	// email the mail provider does not take is still tried again.
	// Default 24h.
	GiveUp time.Duration
}

// schedule reads the settings of emails sent later.
func schedule(getenv func(string) string) (Schedule, error) {
	s := Schedule{Ahead: 30 * 24 * time.Hour}
	if v := strings.ToLower(strings.TrimSpace(getenv("CAIRNPOST_SCHEDULE_AHEAD"))); v != "" {
		ahead, ok := span(v)
		if !ok || ahead < time.Hour || ahead > 366*24*time.Hour {
			return s, fmt.Errorf("config: CAIRNPOST_SCHEDULE_AHEAD must be a time such as 30d or 72h, between 1h and 366d (got %q)", v)
		}
		s.Ahead = ahead
	}
	var err error
	if s.MaxBytes, err = number(getenv, "CAIRNPOST_SCHEDULE_MAX_BYTES", 1<<30); err != nil {
		return s, err
	}
	if s.MaxBytes < 1<<20 {
		return s, fmt.Errorf("config: CAIRNPOST_SCHEDULE_MAX_BYTES must be at least 1048576 (got %d)", s.MaxBytes)
	}
	if s.Poll, err = duration(getenv, "CAIRNPOST_SCHEDULE_POLL", 5*time.Second); err != nil {
		return s, err
	}
	if s.Poll < time.Second || s.Poll > time.Minute {
		return s, fmt.Errorf("config: CAIRNPOST_SCHEDULE_POLL must be between 1s and 1m (got %s)", s.Poll)
	}
	if s.GiveUp, err = duration(getenv, "CAIRNPOST_SCHEDULE_GIVE_UP", 24*time.Hour); err != nil {
		return s, err
	}
	if s.GiveUp < time.Minute || s.GiveUp > 7*24*time.Hour {
		return s, fmt.Errorf("config: CAIRNPOST_SCHEDULE_GIVE_UP must be between 1m and 168h (got %s)", s.GiveUp)
	}
	return s, nil
}
