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
		"CAIRNPOST_POSTGRES_DSN":         {"CAIRNPOST_DRIVER", "capture"},
		"CAIRNPOST_DRIVER":               {"CAIRNPOST_POSTGRES_DSN", "x"},
		"CAIRNPOST_SEND_TIMEOUT":         {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_SEND_TIMEOUT", "soon"},
		"CAIRNPOST_MAX_BODY_BYTES":       {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_MAX_BODY_BYTES", "-1"},
		"CAIRNPOST_IN_DOUBT_WINDOW":      {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_IN_DOUBT_WINDOW", "5s"},
		"CAIRNPOST_CLOUDFLARE_API_TOKEN": {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "cloudflare", "CAIRNPOST_CLOUDFLARE_ACCOUNT_ID", "abc"},
		"would be forgotten":             {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_IDEMPOTENCY_TTL", "1m"},
		"CAIRNPOST_IDEMPOTENCY_TTL":      {"CAIRNPOST_POSTGRES_DSN", "x", "CAIRNPOST_DRIVER", "capture", "CAIRNPOST_IDEMPOTENCY_TTL", "0s"},
	}
	for name, pairs := range cases {
		if _, err := Load(env(pairs...)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWebhookSettings(t *testing.T) {
	base := []string{"CAIRNPOST_POSTGRES_DSN", "postgres://x", "CAIRNPOST_DRIVER", "capture"}
	c, err := Load(env(base...))
	if err != nil || c.Webhook.Timeout != 10*time.Second || c.Webhook.Workers != 8 || c.Webhook.PerEndpoint != 2 || c.Webhook.MaxEndpoints != 20 ||
		c.Webhook.Poll != 2*time.Second || c.Webhook.AllowPrivate || len(c.Webhook.Retries) != 7 ||
		c.Webhook.Retries[0] != 5*time.Second || c.Webhook.Retries[6] != 10*time.Hour {
		t.Fatalf("defaults: %+v %v", c.Webhook, err)
	}
	c, err = Load(env(append(base, "CAIRNPOST_WEBHOOK_RETRIES", "1s, 2m", "CAIRNPOST_WEBHOOK_ALLOW_PRIVATE", "TRUE")...))
	if err != nil || len(c.Webhook.Retries) != 2 || c.Webhook.Retries[1] != 2*time.Minute || !c.Webhook.AllowPrivate {
		t.Fatalf("set: %+v %v", c.Webhook, err)
	}
	for name, value := range map[string]string{
		"CAIRNPOST_WEBHOOK_RETRIES": "5s,soon", "CAIRNPOST_WEBHOOK_ALLOW_PRIVATE": "yes",
		"CAIRNPOST_WEBHOOK_WORKERS": "65", "CAIRNPOST_WEBHOOK_TIMEOUT": "0s", "CAIRNPOST_WEBHOOK_PER_ENDPOINT": "9",
		"CAIRNPOST_WEBHOOK_MAX_ENDPOINTS": "1001",
	} {
		if _, err := Load(env(append(base, name, value)...)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: %v", name, value, err)
		}
	}
}
