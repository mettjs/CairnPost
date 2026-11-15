package memstore

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/mettjs/cairnpost/internal/store"
)

// copyOf is a template nobody else holds a part of.
func copyOf(t store.Template) store.Template {
	t.Defaults, t.Variables = maps.Clone(t.Defaults), slices.Clone(t.Variables)
	if t.Defaults == nil {
		t.Defaults = map[string]string{}
	}
	return t
}

// CreateTemplate implements store.Templates.
func (s *Store) CreateTemplate(_ context.Context, t store.Template, max int) (store.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	had := 0
	for _, other := range s.templates {
		if other.Domain != t.Domain {
			continue
		}
		if had++; other.Name == t.Name {
			return store.Template{}, store.ErrTaken
		}
	}
	if max != 0 && had >= max {
		return store.Template{}, store.ErrLimit
	}
	t = copyOf(t)
	t.ID, t.CreatedAt = s.next("template"), s.Now()
	t.UpdatedAt = t.CreatedAt
	s.templates[t.ID] = t
	return copyOf(t), nil
}

// Template implements store.Templates.
func (s *Store) Template(_ context.Context, id string) (store.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.templates[id]
	if !ok {
		return store.Template{}, store.ErrNotFound
	}
	return copyOf(t), nil
}

// TemplateNamed implements store.Templates.
func (s *Store) TemplateNamed(_ context.Context, domain, name string) (store.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *store.Template
	for _, t := range s.templates {
		if t.Name == name && (t.Domain == domain || t.Domain == "" && found == nil) {
			found = &t
			if t.Domain == domain {
				break
			}
		}
	}
	if found == nil {
		return store.Template{}, store.ErrNotFound
	}
	return copyOf(*found), nil
}

// ListTemplates implements store.Templates.
func (s *Store) ListTemplates(_ context.Context, domain string, limit int) ([]store.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Template
	for _, t := range s.templates {
		if domain == "" || t.Domain == domain {
			t.HTML, t.Text = "", ""
			out = append(out, copyOf(t))
		}
	}
	slices.SortFunc(out, func(a, b store.Template) int {
		return cmp.Or(cmp.Compare(a.Domain, b.Domain), cmp.Compare(a.Name, b.Name))
	})
	return out[:min(len(out), limit)], nil
}

// UpdateTemplate implements store.Templates.
func (s *Store) UpdateTemplate(_ context.Context, t store.Template) (store.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	was, ok := s.templates[t.ID]
	if !ok {
		return store.Template{}, store.ErrNotFound
	}
	if !was.UpdatedAt.Equal(t.UpdatedAt) {
		return store.Template{}, store.ErrStale
	}
	for _, other := range s.templates {
		if other.ID != t.ID && other.Domain == was.Domain && other.Name == t.Name {
			return store.Template{}, store.ErrTaken
		}
	}
	t = copyOf(t)
	// Past the time before, however the clock stands: each change is
	// told apart.
	t.Domain, t.CreatedAt, t.UpdatedAt = was.Domain, was.CreatedAt, s.Now()
	if !t.UpdatedAt.After(was.UpdatedAt) {
		t.UpdatedAt = was.UpdatedAt.Add(time.Microsecond)
	}
	s.templates[t.ID] = t
	return copyOf(t), nil
}

// DeleteTemplate implements store.Templates.
func (s *Store) DeleteTemplate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.templates[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.templates, id)
	return nil
}
