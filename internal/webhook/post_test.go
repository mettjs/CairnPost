package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var at = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// A receiver checks a webhook as the scheme says: the HMAC, under the key
// inside the secret it was shown, of "id.timestamp.body".
func verify(t *testing.T, secret string, h http.Header, prefix string, body []byte) bool {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("the secret is not in the form libraries take: %q", secret)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(h.Get(prefix+"id") + "." + h.Get(prefix+"timestamp") + "."))
	mac.Write(body)
	return h.Get(prefix+"signature") == "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestAWebhookIsSignedUnderBothNamesAndFreshOnEachTry(t *testing.T) {
	var (
		headers []http.Header
		bodies  [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		headers, bodies = append(headers, r.Header.Clone()), append(bodies, b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	now := at
	p := NewPoster(time.Second, true, 2, "test", func() time.Time { return now })
	key := newSecret()
	for range 2 {
		if r := p.Post(context.Background(), srv.URL, [][]byte{key}, "dlv-1", []byte(`{"type":"email.bounced"}`)); !r.Delivered() || r.Status != 204 {
			t.Fatalf("%+v", r)
		}
		now = now.Add(10 * time.Minute)
	}
	for i, h := range headers {
		for _, prefix := range []string{"svix-", "webhook-"} {
			if !verify(t, Reveal(key), h, prefix, bodies[i]) || h.Get(prefix+"id") != "dlv-1" {
				t.Errorf("try %d does not check under %s", i, prefix)
			}
		}
		if h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "CairnPost/test" {
			t.Errorf("headers: %v", h)
		}
	}
	// The id stays; the time, and so the signature, is each try's own.
	if headers[0].Get("svix-timestamp") == headers[1].Get("svix-timestamp") ||
		headers[0].Get("svix-signature") == headers[1].Get("svix-signature") {
		t.Fatal("the second try was signed with the first try's time")
	}
	if len(newSecret()) != 32 || string(newSecret()) == string(key) {
		t.Fatal("signing keys must be 32 random bytes")
	}
}

func TestWhatATryMeetsIsNamedFromAClosedSet(t *testing.T) {
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := int(status.Load())
		if answer == 0 {
			time.Sleep(300 * time.Millisecond)
			return
		}
		if answer == http.StatusFound {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(answer)
		io.WriteString(w, "the endpoint's own words")
	}))
	defer srv.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer secure.Close()
	open := NewPoster(100*time.Millisecond, true, 2, "test", time.Now)
	for name, c := range map[string]struct {
		poster *Poster
		url    string
		answer int
		want   Result
	}{
		"taken":                  {open, srv.URL, 200, Result{200, ""}},
		"an error":               {open, srv.URL, 500, Result{500, FailStatus}},
		"gone":                   {open, srv.URL, 410, Result{410, FailStatus}},
		"a redirect, unfollowed": {open, srv.URL, 302, Result{302, FailStatus}},
		"no answer in time":      {open, srv.URL, 0, Result{0, FailTimeout}},
		"nothing listening":      {open, "http://127.0.0.1:1/", 200, Result{0, FailRefused}},
		// A real lookup, given time: a slow resolver must not read as a timeout.
		"a name that is none":   {NewPoster(10*time.Second, true, 2, "test", time.Now), "http://no-such-host.invalid/", 200, Result{0, FailDNS}},
		"a certificate unknown": {open, secure.URL, 200, Result{0, FailTLS}},
		// The guarded poster refuses this machine itself, whatever the URL says.
		"a private address":  {NewPoster(time.Second, false, 2, "test", time.Now), srv.URL, 200, Result{0, FailBlocked}},
		"this machine by v6": {NewPoster(time.Second, false, 2, "test", time.Now), "http://[::ffff:127.0.0.1]:80/", 200, Result{0, FailBlocked}},
	} {
		status.Store(int32(c.answer))
		if got := c.poster.Post(context.Background(), c.url, [][]byte{[]byte("k")}, "id", []byte("{}")); got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}

func TestOnlyPublicAddressesAreReached(t *testing.T) {
	for address, want := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1:248:1893:25c8:1946": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "255.255.255.255": false,
		"224.0.0.1": false, "198.18.0.1": false, "192.0.0.8": false,
		"::1": false, "::": false, "fe80::1": false, "fd00::1": false, "ff02::1": false,
		"::ffff:10.0.0.1": false, "::ffff:169.254.169.254": false, "64:ff9b::a00:1": false, "2002:a00:1::": false,
		"fec0::1": false, "::10.0.0.1": false, "::127.0.0.1": false, "::ffff:0:10.0.0.1": false,
		"168.63.129.16": false, "2001:0:a00:1::": false, "2001:db8::1": false, "203.0.113.5": false,
		"168.63.129.17": true, "2001:4860:4860::8888": true, "8.8.8.8": true,
	} {
		if got := public(netip.MustParseAddr(address)); got != want {
			t.Errorf("%s: public %v, want %v", address, got, want)
		}
	}
}
