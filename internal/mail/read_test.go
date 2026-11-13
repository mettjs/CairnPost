package mail_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mettjs/cairnpost/internal/mail"
	"github.com/mettjs/cairnpost/internal/store"
)

func TestWhoMayReadAnEmail(t *testing.T) {
	m := store.Message{KeyID: "key-1", Sender: "app@Mail.Example.com"}
	for name, c := range map[string]struct {
		key  store.Key
		want bool
	}{
		"its sender":                   {store.Key{ID: "key-1", Scope: store.ScopeSend}, true},
		"another sender":               {store.Key{ID: "key-2", Scope: store.ScopeSend}, false},
		"another sender of its domain": {store.Key{ID: "key-2", Scope: store.ScopeSend, Domain: "mail.example.com"}, false},
		"an admin":                     {store.Key{ID: "key-3", Scope: store.ScopeAdmin}, true},
		"its domain's admin":           {store.Key{ID: "key-3", Scope: store.ScopeAdmin, Domain: "mail.example.com"}, true},
		"another domain's admin":       {store.Key{ID: "key-3", Scope: store.ScopeAdmin, Domain: "example.com"}, false},
		"a domain that ends alike":     {store.Key{ID: "key-3", Scope: store.ScopeAdmin, Domain: "xmail.example.com"}, false},
	} {
		if got := mail.MayRead(c.key, m); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// The list is an admin's: all of it, or its sending domain's. What is
// asked for is checked before the store is asked.
func TestEmailsAreListedForAnAdmin(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	for _, from := range []string{"app@example.com", "app@other.example", "app@example.com"} {
		req := request("")
		req.From = from
		if _, err := r.send(req); err != nil {
			t.Fatal(err)
		}
	}
	admin := store.Key{ID: "ops", Scope: store.ScopeAdmin}
	count := func(key store.Key, q mail.Query) int {
		t.Helper()
		list, err := r.svc.List(ctx, key, q)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	if n := count(admin, mail.Query{Limit: 10}); n != 3 {
		t.Fatalf("an admin lists %d of 3", n)
	}
	if n := count(store.Key{ID: "ops", Scope: store.ScopeAdmin, Domain: "other.example"}, mail.Query{Limit: 10}); n != 1 {
		t.Fatalf("a domain's admin lists %d of its 1", n)
	}
	if n := count(r.key, mail.Query{Limit: 10}); n != 0 {
		t.Fatalf("a key that only sends lists %d", n)
	}
	if n := count(admin, mail.Query{To: "Ana <ANA@example.org>", State: store.StateSent, Limit: 10}); n != 3 {
		t.Fatalf("by address and state: %d", n)
	}
	if n := count(admin, mail.Query{Limit: 0}); n != 1 {
		t.Fatalf("no limit asked for lists %d, want the least", n)
	}
	if n := count(admin, mail.Query{Limit: 1 << 30}); n != 3 {
		t.Fatalf("a huge limit: %d", n)
	}
	for _, bad := range []mail.Query{{To: "ana"}, {To: "a@b.example, c@d.example"}, {State: "nonsense"}} {
		var invalid *mail.Invalid
		if _, err := r.svc.List(ctx, admin, bad); !errors.As(err, &invalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}
