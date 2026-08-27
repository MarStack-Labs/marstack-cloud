package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var (
	errNotFound  = errors.New("project not found")
	errNameTaken = errors.New("project name already exists")
)

const columns = `id, name, created_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, p Project) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO projects (`+columns+`) VALUES (?, ?, ?)`,
		p.ID, p.Name, p.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

func (r *repository) byID(ctx context.Context, id string) (Project, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM projects WHERE id = ?`, id)
	return scanRow(row)
}

func (r *repository) byName(ctx context.Context, name string) (Project, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM projects WHERE name = ?`, name)
	return scanRow(row)
}

func (r *repository) list(ctx context.Context) ([]Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM projects ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0, 4)
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(row *sql.Row) (Project, error) {
	p, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, errNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("read project: %w", err)
	}
	return p, nil
}

func scan(row scanner) (Project, error) {
	var (
		p          Project
		createdRaw string
	)
	if err := row.Scan(&p.ID, &p.Name, &createdRaw); err != nil {
		return Project{}, err
	}

	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return Project{}, fmt.Errorf("parse project created_at: %w", err)
	}
	p.CreatedAt = created
	return p, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
