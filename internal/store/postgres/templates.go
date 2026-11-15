package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mettjs/cairnpost/internal/store"
)

const templateColumns = `id::text, domain, name, subject, html, body_text, variables, defaults, created_at, updated_at`

// listedColumns are a template's without its bodies.
const listedColumns = `id::text, domain, name, subject, '', '', variables, defaults, created_at, updated_at`

func scanTemplate(row pgx.Row) (store.Template, error) {
	var t store.Template
	err := row.Scan(&t.ID, &t.Domain, &t.Name, &t.Subject, &t.HTML, &t.Text, &t.Variables, &t.Defaults, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Template{}, store.ErrNotFound
	}
	return t, err
}

// nameTaken reports whether an error is a name its domain already has.
func nameTaken(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// defaults are a template's defaults as they are kept: never null.
func defaults(t store.Template) map[string]string {
	if t.Defaults == nil {
		return map[string]string{}
	}
	return t.Defaults
}

// names are a template's variables as they are kept: never null.
func names(t store.Template) []string {
	if t.Variables == nil {
		return []string{}
	}
	return t.Variables
}

// CreateTemplate implements store.Templates.
func (db *DB) CreateTemplate(ctx context.Context, t store.Template, max int) (store.Template, error) {
	var out store.Template
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) (err error) {
		// One at a time: the count is read and the row written as one.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cairnpost.templates'))`); err != nil {
			return err
		}
		// A name the domain has is said before the count is: the answer
		// is the same however many there are.
		var had bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM templates WHERE domain = $1 AND name = $2)`, t.Domain, t.Name).Scan(&had); err != nil {
			return err
		}
		if had {
			return store.ErrTaken
		}
		out, err = scanTemplate(tx.QueryRow(ctx, `
			INSERT INTO templates (domain, name, subject, html, body_text, variables, defaults)
			SELECT $1, $2, $3, $4, $5, $6, $7
			WHERE $8 = 0 OR (SELECT count(*) FROM templates WHERE domain = $1) < $8
			RETURNING `+templateColumns, t.Domain, t.Name, t.Subject, t.HTML, t.Text, names(t), defaults(t), max))
		return err
	})
	switch {
	case nameTaken(err), errors.Is(err, store.ErrTaken):
		return store.Template{}, store.ErrTaken
	case errors.Is(err, store.ErrNotFound):
		return store.Template{}, store.ErrLimit // nothing was inserted: the count was reached
	case err != nil:
		return store.Template{}, fmt.Errorf("postgres: create a template: %w", err)
	}
	return out, nil
}

// Template implements store.Templates.
func (db *DB) Template(ctx context.Context, id string) (store.Template, error) {
	if !uuidShape.MatchString(id) {
		return store.Template{}, store.ErrNotFound
	}
	t, err := scanTemplate(db.pool.QueryRow(ctx, `SELECT `+templateColumns+` FROM templates WHERE id = $1::uuid`, id))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Template{}, fmt.Errorf("postgres: find a template: %w", err)
	}
	return t, err
}

// TemplateNamed implements store.Templates. The domain's own comes
// before the one that serves every domain: ” sorts first, so last here.
func (db *DB) TemplateNamed(ctx context.Context, domain, name string) (store.Template, error) {
	t, err := scanTemplate(db.pool.QueryRow(ctx, `SELECT `+templateColumns+` FROM templates
		WHERE name = $2 AND domain IN ($1, '') ORDER BY domain DESC LIMIT 1`, domain, name))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Template{}, fmt.Errorf("postgres: find a template by name: %w", err)
	}
	return t, err
}

// ListTemplates implements store.Templates.
func (db *DB) ListTemplates(ctx context.Context, domain string, limit int) ([]store.Template, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+listedColumns+` FROM templates
		WHERE $1 = '' OR domain = $1 ORDER BY domain, name LIMIT $2`, domain, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list templates: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Template, error) { return scanTemplate(row) })
	if err != nil {
		return nil, fmt.Errorf("postgres: list templates: %w", err)
	}
	return out, nil
}

// UpdateTemplate implements store.Templates.
func (db *DB) UpdateTemplate(ctx context.Context, t store.Template) (store.Template, error) {
	if !uuidShape.MatchString(t.ID) {
		return store.Template{}, store.ErrNotFound
	}
	// Written only over what was read: of two changes made at one time,
	// the second is told, and neither undoes the other. The time is
	// stamped past the one before, so each change is told apart.
	out, err := scanTemplate(db.pool.QueryRow(ctx, `
		UPDATE templates SET name = $2, subject = $3, html = $4, body_text = $5, variables = $6, defaults = $7,
			updated_at = greatest(now(), updated_at + interval '1 microsecond')
		WHERE id = $1::uuid AND updated_at = $8 RETURNING `+templateColumns,
		t.ID, t.Name, t.Subject, t.HTML, t.Text, names(t), defaults(t), t.UpdatedAt))
	switch {
	case nameTaken(err):
		return store.Template{}, store.ErrTaken
	case errors.Is(err, store.ErrNotFound):
		if _, gone := db.Template(ctx, t.ID); gone == nil {
			return store.Template{}, store.ErrStale
		}
		return store.Template{}, store.ErrNotFound
	case err != nil:
		return store.Template{}, fmt.Errorf("postgres: change a template: %w", err)
	}
	return out, nil
}

// DeleteTemplate implements store.Templates.
func (db *DB) DeleteTemplate(ctx context.Context, id string) error {
	if !uuidShape.MatchString(id) {
		return store.ErrNotFound
	}
	tag, err := db.pool.Exec(ctx, `DELETE FROM templates WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete a template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
