package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

// After its signing secret is changed, an endpoint's webhooks check out
// under the new secret and, for the overlap, under the old one too; after
// it, under the new one alone.
func TestAChangedSecretSignsBesideTheOldOneForAWhile(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, true)
	var (
		headers []http.Header
		bodies  [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		headers, bodies = append(headers, req.Header.Clone()), append(bodies, b)
	}))
	defer srv.Close()
	e, _ := r.svc.Create(ctx, "", webhook.New{URL: srv.URL})
	old := webhook.Reveal(e.Secret)

	changed, err := r.svc.Rotate(ctx, "", e.ID)
	fresh := webhook.Reveal(changed.Secret)
	if err != nil || fresh == old || changed.PreviousUntil == nil || !changed.PreviousUntil.Equal(r.now.Add(overlap)) {
		t.Fatalf("%+v %v", changed, err)
	}
	// checks reports whether the latest webhook carries a signature that
	// matches under a secret, among however many it carries.
	checks := func(secret string) bool {
		h, body := headers[len(headers)-1], bodies[len(bodies)-1]
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(h.Get("webhook-id") + "." + h.Get("webhook-timestamp") + "."))
		mac.Write(body)
		want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		return slices.Contains(strings.Fields(h.Get("webhook-signature")), want)
	}
	r.bounce("example.com", "up-1")
	r.pass()
	if !checks(fresh) || !checks(old) || headers[0].Get("svix-signature") != headers[0].Get("webhook-signature") {
		t.Fatalf("within the overlap: new %v, old %v", checks(fresh), checks(old))
	}
	if !strings.HasPrefix(headers[0].Get("webhook-signature"), "v1,") || len(strings.Fields(headers[0].Get("webhook-signature"))) != 2 {
		t.Fatalf("the signatures: %q", headers[0].Get("webhook-signature"))
	}
	// The test webhook is signed the same way.
	if res, err := r.svc.Test(ctx, "", e.ID); err != nil || !res.Delivered() || !checks(fresh) || !checks(old) {
		t.Fatalf("the test webhook: %+v %v", res, err)
	}

	// Changed again an hour later (the new one was never copied, say):
	// the first secret, which the receiver still holds, goes on signing
	// for its own day, beside the second and the third.
	r.now = r.now.Add(time.Hour)
	again, err := r.svc.Rotate(ctx, "", e.ID)
	newest := webhook.Reveal(again.Secret)
	if err != nil || len(again.Previous) != 2 || !again.PreviousUntil.Equal(r.now.Add(overlap)) {
		t.Fatalf("%+v %v", again, err)
	}
	r.bounce("example.com", "up-again")
	r.pass()
	if !checks(newest) || !checks(fresh) || !checks(old) {
		t.Fatalf("after a second change: newest %v, second %v, first %v", checks(newest), checks(fresh), checks(old))
	}

	// The first secret's day over, it no longer signs; the second still
	// does, for the hour it has left.
	r.now = r.now.Add(overlap - time.Hour)
	r.bounce("example.com", "up-2")
	r.pass()
	if !checks(newest) || !checks(fresh) || checks(old) {
		t.Fatalf("after the first overlap: newest %v, second %v, first %v", checks(newest), checks(fresh), checks(old))
	}
	r.now = r.now.Add(time.Hour)
	r.bounce("example.com", "up-3")
	r.pass()
	if !checks(newest) || checks(fresh) || checks(old) {
		t.Fatalf("after both overlaps: newest %v, second %v, first %v", checks(newest), checks(fresh), checks(old))
	}
	if got, _ := r.svc.Get(ctx, "", e.ID); got.Previous != nil || got.PreviousUntil != nil {
		t.Fatalf("an old key is still answered: %+v", got.PreviousUntil)
	}
	if n, err := r.store.ForgetSecrets(ctx, 10); err != nil || n != 2 {
		t.Fatalf("forgetting the old keys: %d %v", n, err)
	}

	// Where the overlap is off, the old secret stops signing at once.
	sharp := webhook.NewService(r.store, webhook.NewPoster(time.Second, true, 2, "test", func() time.Time { return r.now }), true, 3, 0,
		func() time.Time { return r.now })
	cut, err := sharp.Rotate(ctx, "", e.ID)
	if err != nil || cut.Previous != nil || cut.PreviousUntil != nil {
		t.Fatalf("with no overlap: %+v %v", cut, err)
	}

	// Another domain's key cannot change it, and learns nothing.
	if _, err := r.svc.Rotate(ctx, "other.example", e.ID); err != store.ErrNotFound {
		t.Fatalf("a bound caller: %v", err)
	}
	if _, err := r.svc.Rotate(ctx, "", "nothing"); err != store.ErrNotFound {
		t.Fatalf("no such endpoint: %v", err)
	}
}
