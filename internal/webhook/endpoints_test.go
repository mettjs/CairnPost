package webhook_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
	"github.com/mettjs/cairnpost/internal/webhook"
)

func TestAnEndpointThatMakesNoSenseIsRefusedAndSaysWhy(t *testing.T) {
	r, ctx := newRig(t, false), context.Background()
	var invalid *webhook.Invalid
	for name, n := range map[string]webhook.New{
		"no url":              {},
		"plain http":          {URL: "http://app.example.com/hook"},
		"another scheme":      {URL: "ftp://app.example.com/hook"},
		"no host":             {URL: "https:///hook"},
		"a password in it":    {URL: "https://user:pass@app.example.com/hook"},
		"a fragment":          {URL: "https://app.example.com/hook#top"},
		"a line break":        {URL: "https://app.example.com/ho\nok"},
		"too long":            {URL: "https://app.example.com/" + strings.Repeat("a", 2100)},
		"an event unknown":    {URL: "https://app.example.com/hook", Types: []string{"email.opened"}},
		"a domain that is no": {URL: "https://app.example.com/hook", Domain: "app@example.com"},
		"a long description":  {URL: "https://app.example.com/hook", Description: strings.Repeat("d", 201)},
	} {
		if _, err := r.svc.Create(ctx, "", n); !errors.As(err, &invalid) || invalid.Reason == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	e, err := r.svc.Create(ctx, "", webhook.New{URL: "https://app.example.com/hook?token=t", Domain: "Mail.Example.com",
		Types: []string{"email.bounced", "email.complained", "email.bounced"}, Description: " billing "})
	if err != nil || e.Domain != "mail.example.com" || len(e.Kinds) != 2 || e.Description != "billing" || len(e.Secret) != 32 || !e.Enabled {
		t.Fatalf("%+v %v", e, err)
	}
	bad := "http://app.example.com/hook"
	if _, err := r.svc.Update(ctx, "", e.ID, webhook.Change{URL: &bad}); !errors.As(err, &invalid) {
		t.Fatalf("changing to plain http: %v", err)
	}
	// Where the operator allows it, plain http is an endpoint too.
	if _, err := newRig(t, true).svc.Create(ctx, "", webhook.New{URL: bad}); err != nil {
		t.Fatalf("http where allowed: %v", err)
	}
}

// A caller bound to a sending domain manages that domain's endpoints; any
// other is, to it, not there.
func TestABoundCallerManagesItsOwnDomainsEndpoints(t *testing.T) {
	r, ctx := newRig(t, false), context.Background()
	const url = "https://app.example.com/hook"
	theirs, _ := r.svc.Create(ctx, "", webhook.New{URL: url, Domain: "other.example"})
	everyones, _ := r.svc.Create(ctx, "", webhook.New{URL: url})
	if _, err := r.svc.Create(ctx, "example.com", webhook.New{URL: url, Domain: "other.example"}); !errors.Is(err, webhook.ErrDomain) {
		t.Fatalf("making another domain's: %v", err)
	}
	mine, err := r.svc.Create(ctx, "example.com", webhook.New{URL: url})
	if err != nil || mine.Domain != "example.com" {
		t.Fatalf("its own, with no domain named: %+v %v", mine, err)
	}
	if list, _ := r.svc.List(ctx, "example.com"); len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("its list: %+v", list)
	}
	off := false
	for _, e := range []store.Endpoint{theirs, everyones} {
		for what, err := range map[string]error{
			"read": func() error { _, err := r.svc.Get(ctx, "example.com", e.ID); return err }(),
			"change": func() error {
				_, err := r.svc.Update(ctx, "example.com", e.ID, webhook.Change{Enabled: &off})
				return err
			}(),
			"deliveries": func() error { _, _, err := r.svc.Deliveries(ctx, "example.com", e.ID, "", "", 0); return err }(),
			"retry":      r.svc.Retry(ctx, "example.com", e.ID, "dlv"),
			"test":       func() error { _, err := r.svc.Test(ctx, "example.com", e.ID); return err }(),
			"delete":     r.svc.Delete(ctx, "example.com", e.ID),
		} {
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s of an endpoint not its own: %v", what, err)
			}
		}
	}
	if err := r.svc.Delete(ctx, "example.com", mine.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := r.svc.List(ctx, ""); len(all) != 2 {
		t.Fatalf("the others are untouched: %+v", all)
	}
}

func TestAnEndpointCanBeTriedOut(t *testing.T) {
	r, app := newRig(t, true), newReceiver(t)
	ctx := context.Background()
	e, _ := r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL})
	got, err := r.svc.Test(ctx, "", e.ID)
	if err != nil || !got.Delivered() || got.Status != 200 || len(app.bodies) != 1 ||
		!strings.Contains(app.bodies[0], `"type":"webhook.test"`) || !strings.HasPrefix(app.got[0], "test_") {
		t.Fatalf("%+v %v %v", got, err, app.bodies)
	}
	app.answer(500)
	if got, _ := r.svc.Test(ctx, "", e.ID); got.Delivered() || got.Status != 500 || got.Failure != webhook.FailStatus {
		t.Fatalf("an endpoint that fails: %+v", got)
	}
	// Nothing is kept of a test.
	if list := r.deliveries(e.ID); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

// A sending domain may have so many endpoints, counted for itself: one
// domain's never stand in another's way.
func TestEndpointsAreCountedPerSendingDomain(t *testing.T) {
	r, ctx := newRig(t, false), context.Background()
	const url = "https://app.example.com/hook"
	var invalid *webhook.Invalid
	for i := range 4 {
		_, err := r.svc.Create(ctx, "", webhook.New{URL: url, Domain: "example.com"})
		if full := i == 3; full != errors.As(err, &invalid) {
			t.Fatalf("endpoint %d of a domain allowed 3: %v", i+1, err)
		}
	}
	if !strings.Contains(invalid.Reason, "CAIRNPOST_WEBHOOK_MAX_ENDPOINTS") {
		t.Fatalf("the refusal does not say what to change: %s", invalid.Reason)
	}
	if _, err := r.svc.Create(ctx, "", webhook.New{URL: url, Domain: "other.example"}); err != nil {
		t.Fatalf("another domain's first: %v", err)
	}
	if _, err := r.svc.Create(ctx, "", webhook.New{URL: url}); err != nil {
		t.Fatalf("the first that hears about all: %v", err)
	}
}

func TestDeliveriesAreReadPageByPageAndByState(t *testing.T) {
	r, app := newRig(t, true), newReceiver(t)
	ctx := context.Background()
	e, _ := r.svc.Create(ctx, "", webhook.New{URL: app.srv.URL})
	for i := range 5 {
		r.bounce("example.com", fmt.Sprintf("up-%d", i))
	}
	r.pass()
	app.answer(500)
	r.bounce("example.com", "up-5")
	r.pass()
	var seen []string
	cursor, pages := "", 0
	for {
		page, next, err := r.svc.Deliveries(ctx, "", e.ID, "", cursor, 4)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page {
			seen = append(seen, d.ID)
		}
		pages++
		if cursor = next; cursor == "" {
			break
		}
	}
	if len(seen) != 6 || pages != 2 || slices.Compact(slices.Clone(seen))[0] != seen[0] {
		t.Fatalf("%d pages: %v", pages, seen)
	}
	waiting, _, err := r.svc.Deliveries(ctx, "", e.ID, store.DeliveryPending, "", 0)
	if err != nil || len(waiting) != 1 || waiting[0].ID != seen[0] {
		t.Fatalf("the one still owed, newest first: %+v %v", waiting, err)
	}
	var invalid *webhook.Invalid
	if _, _, err := r.svc.Deliveries(ctx, "", e.ID, "lost", "", 0); !errors.As(err, &invalid) {
		t.Fatalf("a state that is none: %v", err)
	}
	if _, _, err := r.svc.Deliveries(ctx, "", e.ID, "", "a\x00b", 0); !errors.As(err, &invalid) {
		t.Fatalf("a cursor that is none: %v", err)
	}
}

// A URL is kept without the space pasted around it, and refused with one
// inside; a refusal says what the part must be, apart from its name.
func TestAURLIsKeptAsItIsWritten(t *testing.T) {
	r, ctx := newRig(t, false), context.Background()
	e, err := r.svc.Create(ctx, "", webhook.New{URL: "  https://app.example.com/hook?token=abc \n"})
	if err != nil || e.URL != "https://app.example.com/hook?token=abc" {
		t.Fatalf("%q %v", e.URL, err)
	}
	spaced := " https://app.example.com/other\t"
	if e, err = r.svc.Update(ctx, "", e.ID, webhook.Change{URL: &spaced}); err != nil || e.URL != "https://app.example.com/other" {
		t.Fatalf("%q %v", e.URL, err)
	}
	var invalid *webhook.Invalid
	for _, bad := range []string{"https://app.example.com/a b", "https://app.exa mple.com/", "https://app.example.com/a\tb"} {
		if _, err := r.svc.Create(ctx, "", webhook.New{URL: bad}); !errors.As(err, &invalid) || invalid.Field != "url" ||
			invalid.Reason != `"url" `+invalid.Must || invalid.Must == "" {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
