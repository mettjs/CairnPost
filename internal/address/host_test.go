package address_test

import (
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/address"
)

func TestOnlyAHostNameIsAHost(t *testing.T) {
	if host, ok := address.Host(" Mail.Example.COM "); !ok || host != "mail.example.com" {
		t.Errorf("%q %v", host, ok)
	}
	for _, bad := range []string{"", "localhost", "a@b.example", "example.com.", "exa mple.com", "example.com/x", "example.com:25",
		"-a.example.com", "a..example.com", "a_b.example.com", "[192.0.2.1]", strings.Repeat("a", 64) + ".example.com",
		strings.Repeat("a.", 130) + "com", "ex\x00ample.com", "\u043f\u0440\u0438\u043c\u0435\u0440.example"} {
		if _, ok := address.Host(bad); ok {
			t.Errorf("%q is taken for a host name", bad)
		}
	}
}
