package config

import (
	"strings"
	"testing"
	"time"
)

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env("CAIRNPOST_POSTGRES_DSN", "postgres://x", "CAIRNPOST_DRIVER", "capture"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.IdempotencyTTL != 24*time.Hour || c.InDoubtWindow != 2*time.Minute ||
		c.SendTimeout != 10*time.Second || c.MaxBodyBytes != 1<<20 || c.CaptureKeep != 100 {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestWhatCannotRunIsRefusedAndSaysWhy(t *testing.T) {
	cases := map[string][]string{
		"CAIRNPOST_POSTGRES_DSN":    {"CAIRNPOST_DRIVER", "capture"},
		"CAIRNPOST_DRIVER":          {"CAIRNPOST_POSTGRES_DSN", "x"},
		"CAIRNPOST_SEND_TIMEOUT":    {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_SEND_TIMEOUT", "soon"},
		"CAIRNPOST_MAX_BODY_BYTES":  {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_MAX_BODY_BYTES", "-1"},
		"CAIRNPOST_IN_DOUBT_WINDOW": {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_IN_DOUBT_WINDOW", "5s"},
		"would be forgotten":        {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_IDEMPOTENCY_TTL", "1m"},
		"CAIRNPOST_IDEMPOTENCY_TTL": {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_IDEMPOTENCY_TTL", "0s"},
	}
	for name, pairs := range cases {
		if _, err := Load(env(pairs...)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
