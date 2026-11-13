package suppress_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/store/memstore"
	"github.com/mettjs/cairnpost/internal/suppress"
)

func TestAddIsOnceAndLiftUndoesIt(t *testing.T) {
	ctx, svc := context.Background(), suppress.New(memstore.New())
	s, added, err := svc.Add(ctx, " Mail.Example.COM ", "Ana@Example.org", "asked us to stop")
	if err != nil || !added || s.Domain != "mail.example.com" || s.Address != "ana@example.org" {
		t.Fatalf("%+v %v %v", s, added, err)
	}
	// However it is typed, it is the same mailbox.
	if _, added, err := svc.Add(ctx, "mail.example.com", "ANA@example.org", ""); err != nil || added {
		t.Fatalf("adding it again: added %v, %v", added, err)
	}
	page, next, err := svc.List(ctx, "", "", 0)
	if err != nil || len(page) != 1 || next != "" || page[0].Reason != store.ReasonManual || page[0].Note != "asked us to stop" {
		t.Fatalf("%+v %q %v", page, next, err)
	}
	if err := svc.Lift(ctx, "mail.example.com", "ana@EXAMPLE.org"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Lift(ctx, "mail.example.com", "ana@example.org"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lifting it twice: %v", err)
	}
}

func TestWhatMakesNoSenseIsRefusedAndSaysWhy(t *testing.T) {
	ctx, svc := context.Background(), suppress.New(memstore.New())
	var invalid *suppress.Invalid
	for name, try := range map[string]func() error{
		"no domain":         func() error { _, _, err := svc.Add(ctx, "", "a@example.org", ""); return err },
		"not a domain":      func() error { _, _, err := svc.Add(ctx, "exa mple", "a@example.org", ""); return err },
		"a note of lines":   func() error { _, _, err := svc.Add(ctx, "example.com", "a@example.org", "a\nb"); return err },
		"a hidden domain":   func() error { _, _, err := svc.List(ctx, "exa\x00mple.com", "", 10); return err },
		"a broken domain":   func() error { _, _, err := svc.List(ctx, "exa\xffmple.com", "", 10); return err },
		"lift all of what":  func() error { _, err := svc.LiftAll(ctx, "", ""); return err },
		"an unknown reason": func() error { _, err := svc.LiftAll(ctx, "example.com", "spam"); return err },
		"a cursor that hides": func() error {
			_, _, err := svc.List(ctx, "", base64.RawURLEncoding.EncodeToString([]byte("a\x00b\x00c")), 10)
			return err
		},
		"an address there": func() error { _, _, err := svc.Add(ctx, "a@example.com", "a@example.org", ""); return err },
		"not an address":   func() error { _, _, err := svc.Add(ctx, "example.com", "nobody", ""); return err },
		"a named address":  func() error { _, _, err := svc.Add(ctx, "example.com", "Ana <a@example.org>", ""); return err },
		"a long note": func() error {
			_, _, err := svc.Add(ctx, "example.com", "a@example.org", string(make([]byte, 501)))
			return err
		},
		"lift nothing":     func() error { return svc.Lift(ctx, "example.com", "") },
		"a huge page":      func() error { _, _, err := svc.List(ctx, "", "", suppress.MaxLimit+1); return err },
		"a made-up cursor": func() error { _, _, err := svc.List(ctx, "", "!!!", 10); return err },
	} {
		if err := try(); !errors.As(err, &invalid) || invalid.Reason == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheListIsReadPageByPage(t *testing.T) {
	ctx, svc := context.Background(), suppress.New(memstore.New())
	for i := range 7 {
		svc.Add(ctx, "a.example.com", fmt.Sprintf("u%d@example.org", i), "")
	}
	svc.Add(ctx, "b.example.com", "u0@example.org", "")
	var seen []string
	cursor, pages := "", 0
	for {
		page, next, err := svc.List(ctx, "", cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page {
			seen = append(seen, s.Domain+" "+s.Address)
		}
		pages++
		if cursor = next; cursor == "" {
			break
		}
	}
	if len(seen) != 8 || pages != 3 || seen[0] != "a.example.com u0@example.org" || seen[7] != "b.example.com u0@example.org" {
		t.Fatalf("%d pages: %v", pages, seen)
	}
	if only, _, _ := svc.List(ctx, "B.example.com", "", 10); len(only) != 1 {
		t.Fatalf("one domain's: %v", only)
	}
}

// Whatever the list shows can be lifted: a domain no pattern foresaw, an
// address kept in a form the parser would write differently.
func TestWhateverTheListShowsCanBeLifted(t *testing.T) {
	ctx, st := context.Background(), memstore.New()
	svc := suppress.New(st)
	for _, row := range [][2]string{
		{"mail_1.example.com", "a@example.org"},     // an underscore
		{"bücher.example", "a@example.org"},         // not ASCII
		{"example.com", `"ana maria"@example.org`},  // quoted
		{"example.com", "not an address as parsed"}, // kept as an upstream gave it
	} {
		st.Suppress(ctx, store.Suppression{Domain: row[0], Address: row[1], Reason: store.ReasonBounce})
	}
	page, _, err := svc.List(ctx, "", "", 0)
	if err != nil || len(page) != 4 {
		t.Fatalf("%+v %v", page, err)
	}
	for _, s := range page {
		if err := svc.Lift(ctx, s.Domain, s.Address); err != nil {
			t.Errorf("lifting %s for %s: %v", s.Address, s.Domain, err)
		}
	}
}

func TestLiftAllIsOneDomainsAndCanBeOneReasons(t *testing.T) {
	ctx, st := context.Background(), memstore.New()
	svc := suppress.New(st)
	for i := range 3 {
		st.Suppress(ctx, store.Suppression{Domain: "a.example.com", Address: fmt.Sprintf("b%d@example.org", i), Reason: store.ReasonBounce})
	}
	svc.Add(ctx, "a.example.com", "hand@example.org", "")
	svc.Add(ctx, "b.example.com", "other@example.org", "")
	if n, err := svc.LiftAll(ctx, "A.example.com", store.ReasonBounce); err != nil || n != 3 {
		t.Fatalf("the bounces: %d %v", n, err)
	}
	if n, err := svc.LiftAll(ctx, "a.example.com", ""); err != nil || n != 1 {
		t.Fatalf("the rest: %d %v", n, err)
	}
	if left, _, _ := svc.List(ctx, "", "", 0); len(left) != 1 || left[0].Domain != "b.example.com" {
		t.Fatalf("another domain's stay: %+v", left)
	}
}
