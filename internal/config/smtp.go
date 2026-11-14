package config

import (
	"fmt"
	"os"
	"strings"
)

// SMTP is the mail server the smtp driver sends through.
type SMTP struct {
	// Host is CAIRNPOST_SMTP_HOST, required for the driver. Port is
	// CAIRNPOST_SMTP_PORT: by default the one its Security is spoken on.
	Host string
	Port int
	// Security is CAIRNPOST_SMTP_SECURITY: "starttls" (the default: the
	// connection is secured before anything is said, or not used),
	// "tls" (secured from the first byte) or "none".
	Security string
	// Username and Password are CAIRNPOST_SMTP_USERNAME and
	// CAIRNPOST_SMTP_PASSWORD, both or neither. The password is a
	// secret: read here, handed to the driver, and written nowhere. It
	// is never sent on a connection that is not secured.
	Username, Password string
	// Hello is CAIRNPOST_SMTP_HELLO, the name the server is greeted
	// with: this machine's by default.
	Hello string
}

// smtpPorts is the port each way of securing is spoken on.
var smtpPorts = map[string]int64{"starttls": 587, "tls": 465, "none": 25}

// smtpOf reads the smtp driver's settings; inUse says they are needed.
func smtpOf(getenv func(string) string, inUse bool) (SMTP, error) {
	s := SMTP{
		Host: strings.TrimSpace(getenv("CAIRNPOST_SMTP_HOST")), Security: strings.ToLower(strings.TrimSpace(getenv("CAIRNPOST_SMTP_SECURITY"))),
		Username: getenv("CAIRNPOST_SMTP_USERNAME"), Password: getenv("CAIRNPOST_SMTP_PASSWORD"),
		Hello: strings.TrimSpace(getenv("CAIRNPOST_SMTP_HELLO")),
	}
	if s.Security == "" {
		s.Security = "starttls"
	}
	fallback, ok := smtpPorts[s.Security]
	if !ok {
		return s, fmt.Errorf("config: CAIRNPOST_SMTP_SECURITY must be starttls, tls or none (got %q)", s.Security)
	}
	port, err := number(getenv, "CAIRNPOST_SMTP_PORT", fallback)
	if err != nil {
		return s, err
	}
	if s.Port = int(port); port > 65535 {
		return s, fmt.Errorf("config: CAIRNPOST_SMTP_PORT must be at most 65535 (got %d)", port)
	}
	switch {
	case inUse && s.Host == "":
		return s, fmt.Errorf("config: the smtp driver needs CAIRNPOST_SMTP_HOST, the mail server to send through")
	case (s.Username == "") != (s.Password == ""):
		return s, fmt.Errorf("config: set CAIRNPOST_SMTP_USERNAME and CAIRNPOST_SMTP_PASSWORD both, or neither")
	case s.Password != "" && s.Security == "none":
		return s, fmt.Errorf("config: CAIRNPOST_SMTP_SECURITY=none with a password would send it in the clear; " +
			"secure the connection (starttls or tls) or sign in to nobody")
	}
	if s.Hello == "" {
		if s.Hello, err = os.Hostname(); err != nil || s.Hello == "" {
			s.Hello = "localhost"
		}
	}
	return s, nil
}
