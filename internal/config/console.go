package config

import (
	"fmt"
	"strings"
	"time"
)

// Console is the management console's settings.
type Console struct {
	// On is CAIRNPOST_CONSOLE: whether the console is served, at /console,
	// by the same server as the API. Default true; "off" leaves it out.
	On bool
	// Session is CAIRNPOST_CONSOLE_SESSION: how long a sign-in lasts.
	// Default 12h.
	Session time.Duration
	// PlainHTTP is CAIRNPOST_CONSOLE_PLAIN_HTTP: let the session cookie
	// travel without TLS, for a console opened over plain http on a
	// developer's machine. Default false: the cookie is marked Secure.
	PlainHTTP bool
}

func console(getenv func(string) string) (Console, error) {
	c := Console{}
	var err error
	if c.On, err = flag(getenv, "CAIRNPOST_CONSOLE", true); err != nil {
		return c, err
	}
	if c.Session, err = duration(getenv, "CAIRNPOST_CONSOLE_SESSION", 12*time.Hour); err != nil {
		return c, err
	}
	if c.Session < time.Minute || c.Session > 30*24*time.Hour {
		return c, fmt.Errorf("config: CAIRNPOST_CONSOLE_SESSION must be between 1m and 720h (got %s)", c.Session)
	}
	c.PlainHTTP, err = flag(getenv, "CAIRNPOST_CONSOLE_PLAIN_HTTP", false)
	return c, err
}

// flag reads a setting that is on or off.
func flag(getenv func(string) string, name string, fallback bool) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(getenv(name))); v {
	case "":
		return fallback, nil
	case "true", "1", "on":
		return true, nil
	case "false", "0", "off":
		return false, nil
	default:
		return false, fmt.Errorf("config: %s must be on or off (got %q)", name, v)
	}
}

// MaxKeys reads CAIRNPOST_MAX_KEYS on its own, for a command that needs
// that one setting and not a whole configuration.
func MaxKeys(getenv func(string) string) (int, error) {
	n, err := number(getenv, "CAIRNPOST_MAX_KEYS", 200)
	if err == nil && n > 100000 {
		err = fmt.Errorf("config: CAIRNPOST_MAX_KEYS must be at most 100000 (got %d)", n)
	}
	return int(n), err
}

// DNSTimeout reads CAIRNPOST_DNS_TIMEOUT on its own, for a command that
// needs that one setting and not a whole configuration.
func DNSTimeout(getenv func(string) string) (time.Duration, error) {
	d, err := duration(getenv, "CAIRNPOST_DNS_TIMEOUT", 6*time.Second)
	if err == nil && (d < time.Second || d > time.Minute) {
		err = fmt.Errorf("config: CAIRNPOST_DNS_TIMEOUT must be between 1s and 1m (got %s)", d)
	}
	return d, err
}
