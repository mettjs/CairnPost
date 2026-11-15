package store

import (
	"context"
	"errors"
	"time"
)

// ErrTaken is a name another already has.
var ErrTaken = errors.New("store: the name is taken")

// Template is an email written once to be sent many times.
type Template struct {
	ID string
	// Domain is the sending domain it belongs to; "" for one that
	// serves every domain.
	Domain string
	// Name is how a send asks for it: one of a kind in its domain.
	Name    string
	Subject string
	HTML    string
	Text    string
	// Defaults are what a variable is filled with when a send gives it
	// no value.
	Defaults  map[string]string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Templates keeps the templates.
type Templates interface {
	// CreateTemplate keeps a new one. ErrLimit when its domain already
	// has max (0 for no limit), ErrTaken when the domain has the name.
	CreateTemplate(ctx context.Context, t Template, max int) (Template, error)
	// Template answers one by id; ErrNotFound for none.
	Template(ctx context.Context, id string) (Template, error)
	// TemplateNamed answers the one a sending domain's email is made
	// from under a name: the domain's own, or else the one that serves
	// every domain. ErrNotFound for none.
	TemplateNamed(ctx context.Context, domain, name string) (Template, error)
	// ListTemplates answers a domain's templates by name, or every
	// template by domain and name for "", limit at most.
	ListTemplates(ctx context.Context, domain string, limit int) ([]Template, error)
	// UpdateTemplate writes what a template is over what it was, by id,
	// but its domain. ErrNotFound for none, ErrTaken for a name its
	// domain has.
	UpdateTemplate(ctx context.Context, t Template) (Template, error)
	// DeleteTemplate removes one; ErrNotFound for none.
	DeleteTemplate(ctx context.Context, id string) error
}
