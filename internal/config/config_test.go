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
	if c.Webhook.PauseAfter != 5*24*time.Hour || c.Webhook.SecretOverlap != 24*time.Hour {
		t.Fatalf("an endpoint is paused after %v and an old secret signs for %v by default, want five days and one",
			c.Webhook.PauseAfter, c.Webhook.SecretOverlap)
	}
	c, err = Load(env(append(base, "CAIRNPOST_WEBHOOK_RETRIES", "1s, 2m", "CAIRNPOST_WEBHOOK_ALLOW_PRIVATE", "TRUE",
		"CAIRNPOST_WEBHOOK_PAUSE_AFTER", "2d")...))
	if err != nil || len(c.Webhook.Retries) != 2 || c.Webhook.Retries[1] != 2*time.Minute || !c.Webhook.AllowPrivate ||
		c.Webhook.PauseAfter != 48*time.Hour {
		t.Fatalf("set: %+v %v", c.Webhook, err)
	}
	if c, err = Load(env(append(base, "CAIRNPOST_WEBHOOK_PAUSE_AFTER", "off", "CAIRNPOST_WEBHOOK_SECRET_OVERLAP", "off")...)); err != nil ||
		c.Webhook.PauseAfter != 0 || c.Webhook.SecretOverlap != 0 {
		t.Fatalf("pausing and the overlap off: %v %v %v", c.Webhook.PauseAfter, c.Webhook.SecretOverlap, err)
	}
	if c, err = Load(env(append(base, "CAIRNPOST_WEBHOOK_SECRET_OVERLAP", "3d")...)); err != nil || c.Webhook.SecretOverlap != 72*time.Hour {
		t.Fatalf("an overlap of three days: %v %v", c.Webhook.SecretOverlap, err)
	}
	for name, value := range map[string]string{
		"CAIRNPOST_WEBHOOK_RETRIES": "5s,soon", "CAIRNPOST_WEBHOOK_ALLOW_PRIVATE": "yes", "CAIRNPOST_MAX_KEYS": "0",
		"CAIRNPOST_WEBHOOK_WORKERS": "65", "CAIRNPOST_WEBHOOK_TIMEOUT": "0s", "CAIRNPOST_WEBHOOK_PER_ENDPOINT": "9",
		"CAIRNPOST_WEBHOOK_MAX_ENDPOINTS": "1001", "CAIRNPOST_WEBHOOK_PAUSE_AFTER": "59m", "CAIRNPOST_WEBHOOK_PAUSE_AFTER ": "never",
		"CAIRNPOST_WEBHOOK_SECRET_OVERLAP": "8d", "CAIRNPOST_WEBHOOK_SECRET_OVERLAP ": "soon",
	} {
		name = strings.TrimSpace(name)
		if _, err := Load(env(append(base, name, value)...)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: %v", name, value, err)
		}
	}
}

func TestRetention(t *testing.T) {
	base := []string{"CAIRNPOST_POSTGRES_DSN", "postgres://x", "CAIRNPOST_DRIVER", "capture"}
	for value, want := range map[string]time.Duration{
		"": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour, "168h": 7 * 24 * time.Hour, " OFF ": 0,
	} {
		c, err := Load(env(append(base, "CAIRNPOST_RETENTION", value)...))
		if err != nil || c.Retention != want || c.PruneEvery != time.Hour {
			t.Errorf("%q: %s every %s, %v", value, c.Retention, c.PruneEvery, err)
		}
	}
	for value, says := range map[string]string{
		"soon": "such as 30d", "0d": "such as 30d", "-3d": "such as 30d", "99999d": "such as 30d", "-213490d": "such as 30d",
		"36501d": "such as 30d", "2000000h": "such as 30d", "1.5d": "such as 30d", "d": "such as 30d",
		"6d": "at least 7d", "24h": "at least 7d",
	} {
		if _, err := Load(env(append(base, "CAIRNPOST_RETENTION", value)...)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v", value, err)
		}
	}
	// What emails say is kept only when asked, for a short time, and never
	// longer than the record.
	for value, want := range map[string]time.Duration{"": 0, "off": 0, "36h": 36 * time.Hour, "30d": 30 * 24 * time.Hour} {
		c, err := Load(env(append(base, "CAIRNPOST_CONTENT_RETENTION", value)...))
		if err != nil || c.ContentRetention != want {
			t.Errorf("content %q: %s %v", value, c.ContentRetention, err)
		}
	}
	for says, pairs := range map[string][]string{
		"such as 24h":         {"CAIRNPOST_CONTENT_RETENTION", "a while"},
		"at most 30d":         {"CAIRNPOST_CONTENT_RETENTION", "31d", "CAIRNPOST_RETENTION", "off"},
		"CAIRNPOST_RETENTION": {"CAIRNPOST_CONTENT_RETENTION", "10d", "CAIRNPOST_RETENTION", "7d"},
	} {
		if _, err := Load(env(append(base, pairs...)...)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("content %v: %v", pairs, err)
		}
	}
	// A key is never remembered longer than its email's record is kept.
	if _, err := Load(env(append(base, "CAIRNPOST_RETENTION", "7d", "CAIRNPOST_IDEMPOTENCY_TTL", "200h")...)); err == nil ||
		!strings.Contains(err.Error(), "CAIRNPOST_IDEMPOTENCY_TTL") {
		t.Errorf("a key outliving its record: %v", err)
	}
	// Nor is a webhook deleted with tries still left.
	if _, err := Load(env(append(base, "CAIRNPOST_RETENTION", "7d", "CAIRNPOST_WEBHOOK_RETRIES", "100h,100h")...)); err == nil ||
		!strings.Contains(err.Error(), "CAIRNPOST_WEBHOOK_RETRIES") {
		t.Errorf("a webhook outliving its record: %v", err)
	}
	if _, err := Load(env(append(base, "CAIRNPOST_PRUNE_EVERY", "never")...)); err == nil || !strings.Contains(err.Error(), "CAIRNPOST_PRUNE_EVERY") {
		t.Errorf("CAIRNPOST_PRUNE_EVERY: %v", err)
	}
}

func TestConsoleSettings(t *testing.T) {
	base := []string{"CAIRNPOST_POSTGRES_DSN", "postgres://x", "CAIRNPOST_DRIVER", "capture"}
	c, err := Load(env(base...))
	if err != nil || !c.Console.On || c.Console.Session != 12*time.Hour || c.Console.PlainHTTP {
		t.Fatalf("defaults: %+v %v", c.Console, err)
	}
	c, err = Load(env(append(base, "CAIRNPOST_CONSOLE", "OFF", "CAIRNPOST_CONSOLE_SESSION", "30m", "CAIRNPOST_CONSOLE_PLAIN_HTTP", "true")...))
	if err != nil || c.Console.On || c.Console.Session != 30*time.Minute || !c.Console.PlainHTTP {
		t.Fatalf("set: %+v %v", c.Console, err)
	}
	c, err = Load(env(append(base, "CAIRNPOST_CONSOLE_SESSION", "1m", "CAIRNPOST_MAX_KEYS", "7", "CAIRNPOST_WEBHOOK_ALLOW_PRIVATE", "on")...))
	if err != nil || c.Console.Session != time.Minute || c.MaxKeys != 7 || !c.Webhook.AllowPrivate {
		t.Fatalf("the shortest session, a key limit, on: %+v %d %v", c.Console, c.MaxKeys, err)
	}
	if c, err = Load(env(base...)); err != nil || c.MaxKeys != 200 || c.DNSTimeout != 6*time.Second {
		t.Fatalf("the defaults: %d %s %v", c.MaxKeys, c.DNSTimeout, err)
	}
	if c, err = Load(env(append(base, "CAIRNPOST_DNS_TIMEOUT", "20s")...)); err != nil || c.DNSTimeout != 20*time.Second {
		t.Fatalf("a DNS timeout: %s %v", c.DNSTimeout, err)
	}
	for _, bad := range [][2]string{
		{"CAIRNPOST_CONSOLE", "maybe"}, {"CAIRNPOST_CONSOLE_SESSION", "721h"}, {"CAIRNPOST_CONSOLE_SESSION", "500ms"},
		{"CAIRNPOST_CONSOLE_PLAIN_HTTP", "yes please"}, {"CAIRNPOST_MAX_KEYS", "100001"}, {"CAIRNPOST_MAX_KEYS", "many"},
		{"CAIRNPOST_DNS_TIMEOUT", "500ms"}, {"CAIRNPOST_DNS_TIMEOUT", "2m"}, {"CAIRNPOST_DNS_TIMEOUT", "soon"},
	} {
		if _, err := Load(env(append(base, bad[0], bad[1])...)); err == nil || !strings.Contains(err.Error(), bad[0]) {
			t.Errorf("%s=%s: %v", bad[0], bad[1], err)
		}
	}
}

// A send is taken to be abandoned after the in-doubt window, and never
// while one could still be under way.
func TestAbandonedAfterIsNeverInsideASend(t *testing.T) {
	for _, c := range []struct{ window, send, want time.Duration }{
		{2 * time.Minute, 10 * time.Second, 2 * time.Minute},
		{11 * time.Second, 10 * time.Second, 70 * time.Second},
		{time.Hour, 10 * time.Second, time.Hour},
	} {
		if got := (Config{InDoubtWindow: c.window, SendTimeout: c.send}).AbandonedAfter(); got != c.want {
			t.Errorf("window %v, send %v: %v, want %v", c.window, c.send, got, c.want)
		}
	}
}

// The DNS server a check asks is an address, with a port or the usual
// one; a name is refused, since the resolver it replaces would have to
// look it up.
func TestDNSServer(t *testing.T) {
	for given, want := range map[string]string{"": "", " 1.1.1.1 ": "1.1.1.1:53", "9.9.9.9:5353": "9.9.9.9:5353",
		"2606:4700:4700::1111": "[2606:4700:4700::1111]:53", "[2606:4700:4700::1111]:53": "[2606:4700:4700::1111]:53"} {
		if got, err := DNSServer(env("CAIRNPOST_DNS_SERVER", given)); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", given, got, err, want)
		}
	}
	for _, bad := range []string{"dns.example.com", "one.one.one.one:53", "1.1.1.1:0", "1.1.1.1:port"} {
		if _, err := DNSServer(env("CAIRNPOST_DNS_SERVER", bad)); err == nil || !strings.Contains(err.Error(), "CAIRNPOST_DNS_SERVER") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
