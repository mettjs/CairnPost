//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mettjs/cairnpost/internal/store"
)

// A template is one of a kind by name in its domain, so many to a
// domain, found by name for a domain before the one that serves all,
// and changed and deleted by id.
func TestTemplatesAreKeptByDomainAndName(t *testing.T) {
	ctx, db := context.Background(), open(t)
	k := key(t, db)
	domain := strings.ToLower(k.Prefix) + ".example"
	name := "t-" + strings.ToLower(k.Prefix)
	mine := store.Template{Domain: domain, Name: name, Subject: "Mine {{a}}", HTML: "<p>{{a}}</p>", Text: "{{a}}", Variables: []string{"a"}, Defaults: map[string]string{"a": "x"}}
	made, err := db.CreateTemplate(ctx, mine, 2)
	if err != nil || made.ID == "" || made.Subject != mine.Subject || made.HTML != mine.HTML || made.Text != mine.Text ||
		made.Defaults["a"] != "x" || made.CreatedAt.IsZero() || !made.UpdatedAt.Equal(made.CreatedAt) {
		t.Fatalf("%+v %v", made, err)
	}
	if _, err := db.CreateTemplate(ctx, mine, 2); !errors.Is(err, store.ErrTaken) {
		t.Errorf("the same name twice: %v", err)
	}
	second := mine
	second.Name, second.Defaults = name+"-2", nil
	if got, err := db.CreateTemplate(ctx, second, 2); err != nil || got.Defaults == nil || len(got.Defaults) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	second.Name = name + "-3"
	if _, err := db.CreateTemplate(ctx, second, 2); !errors.Is(err, store.ErrLimit) {
		t.Errorf("a third where two may be: %v", err)
	}
	// A name the domain has is said to be taken, full or not.
	if _, err := db.CreateTemplate(ctx, mine, 2); !errors.Is(err, store.ErrTaken) {
		t.Errorf("the same name, the domain full: %v", err)
	}
	// The one that serves every domain: found for another domain, and
	// for this one only where it has none of the name.
	all := store.Template{Name: name, Subject: "Everyone's", Text: "t"}
	shared, err := db.CreateTemplate(ctx, all, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DeleteTemplate(ctx, shared.ID) })
	if got, err := db.TemplateNamed(ctx, domain, name); err != nil || got.ID != made.ID {
		t.Errorf("for its own domain: %+v %v", got, err)
	}
	if got, err := db.TemplateNamed(ctx, "other.example", name); err != nil || got.ID != shared.ID {
		t.Errorf("for another domain: %+v %v", got, err)
	}
	if _, err := db.TemplateNamed(ctx, "other.example", name+"-2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another domain's own: %v", err)
	}
	list, err := db.ListTemplates(ctx, domain, 10)
	if err != nil || len(list) != 2 || list[0].Name != name || list[1].Name != name+"-2" || list[0].HTML != "" || list[0].Text != "" ||
		list[0].Subject != mine.Subject || len(list[0].Variables) != 1 || list[0].Variables[0] != "a" || list[0].Defaults["a"] != "x" {
		t.Errorf("%+v %v", list, err)
	}
	if list, err := db.ListTemplates(ctx, domain, 1); err != nil || len(list) != 1 {
		t.Errorf("%+v %v", list, err)
	}
	// A change keeps its domain and when it was made; a name the domain
	// has is refused.
	made.Subject, made.Domain, made.Defaults = "Changed", "elsewhere.example", nil
	changed, err := db.UpdateTemplate(ctx, made)
	if err != nil || changed.Subject != "Changed" || changed.Domain != domain || !changed.CreatedAt.Equal(made.CreatedAt) ||
		changed.UpdatedAt.Before(made.UpdatedAt) || len(changed.Defaults) != 0 {
		t.Errorf("%+v %v", changed, err)
	}
	// Over a read that is no longer the latest, it is told and not made.
	if _, err := db.UpdateTemplate(ctx, made); !errors.Is(err, store.ErrStale) {
		t.Errorf("a change over a stale read: %v", err)
	}
	if again, err := db.UpdateTemplate(ctx, changed); err != nil || !again.UpdatedAt.After(changed.UpdatedAt) {
		t.Errorf("a change over the latest: %+v %v", again, err)
	} else {
		made.UpdatedAt = again.UpdatedAt
	}
	made.Name = name + "-2"
	if _, err := db.UpdateTemplate(ctx, made); !errors.Is(err, store.ErrTaken) {
		t.Errorf("renamed to a name that is taken: %v", err)
	}
	if got, err := db.Template(ctx, made.ID); err != nil || got.Subject != "Changed" || got.Name != name {
		t.Errorf("%+v %v", got, err)
	}
	for _, id := range []string{made.ID, list[1].ID} {
		if err := db.DeleteTemplate(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{made.ID, "not-an-id"} {
		if _, err := db.Template(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
		if err := db.DeleteTemplate(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s deleted: %v", id, err)
		}
		if _, err := db.UpdateTemplate(ctx, store.Template{ID: id, Name: "x", Subject: "s"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s changed: %v", id, err)
		}
	}
}
