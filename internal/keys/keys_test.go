package keys_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/keys"
	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
)

func TestAKeyAuthenticatesUntilRevoked(t *testing.T) {
	ctx, s := context.Background(), memstore.New()
	svc := keys.New(s)
	full, k, err := svc.Create(ctx, "billing", store.ScopeSend, " Example.COM ")
	if err != nil || !strings.HasPrefix(full, k.Prefix+"_") || k.Domain != "example.com" {
		t.Fatalf("create: %q %+v %v", full, k, err)
	}
	if got, err := svc.Authenticate(ctx, full); err != nil || got.ID != k.ID {
		t.Fatalf("the key itself: %v", err)
	}
	for name, wrong := range map[string]string{
		"empty": "", "no lead": "xx_" + full[3:], "short": k.Prefix, "another secret": changed(full, len(full)-1),
		"unknown prefix": changed(full, len(k.Prefix)-1),
	} {
		if _, err := svc.Authenticate(ctx, wrong); !errors.Is(err, keys.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := svc.Revoke(ctx, k.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, full); !errors.Is(err, keys.ErrInvalid) {
		t.Fatalf("a revoked key: %v", err)
	}
	if err := svc.Revoke(ctx, k.Prefix); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoking twice: %v", err)
	}
}

// changed is the key with the character at one place replaced by another:
// always a different string, whatever was drawn.
func changed(full string, at int) string {
	swap := byte('a')
	if full[at] == swap {
		swap = 'b'
	}
	return full[:at] + string(swap) + full[at+1:]
}

func TestTheStoreNeverHoldsTheKey(t *testing.T) {
	ctx, s := context.Background(), memstore.New()
	full, k, _ := keys.New(s).Create(ctx, "x", store.ScopeAdmin, "")
	kept, _ := s.KeyByPrefix(ctx, k.Prefix)
	secret := full[len(k.Prefix)+1:]
	if strings.Contains(string(kept.Hash), secret) || strings.Contains(kept.Prefix+kept.Name, secret) {
		t.Fatal("the stored key holds its own secret")
	}
}

func TestCreateRefusesWhatMakesNoSense(t *testing.T) {
	svc := keys.New(memstore.New())
	if _, _, err := svc.Create(context.Background(), "  ", store.ScopeSend, ""); err == nil {
		t.Error("a nameless key was made")
	}
	if _, _, err := svc.Create(context.Background(), "x", "root", ""); err == nil {
		t.Error("an unknown scope was accepted")
	}
}
