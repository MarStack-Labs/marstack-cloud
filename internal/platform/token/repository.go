package token

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
	errNotFound  = errors.New("token not found")
	errNameTaken = errors.New("token name already exists")
)

const columns = `id, name, role, project_id, secret_hash, created_at, last_used_at, expires_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, t Token, hash string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tokens (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Role, t.ProjectID, hash,
		t.CreatedAt.Format(time.RFC3339Nano), t.LastUsedAt.Format(time.RFC3339Nano),
		stampOf(t.ExpiresAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert token: %w", err)
	}
	return nil
}

func (r *repository) byHash(ctx context.Context, hash string) (Identity, error) {
	var (
		identity Identity
		expires  string
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, role, project_id, expires_at FROM tokens WHERE secret_hash = ?`, hash,
	).Scan(&identity.ID, &identity.Name, &identity.Role, &identity.ProjectID, &expires)

	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, errNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("look up token: %w", err)
	}

	if identity.ExpiresAt, err = parseStamp(expires); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func stampOf(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

func parseStamp(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse expires_at: %w", err)
	}
	return at, nil
}

func (r *repository) touch(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tokens SET last_used_at = ? WHERE id = ?`, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("record token use: %w", err)
	}
	return nil
}

func (r *repository) list(ctx context.Context) ([]Token, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, role, project_id, created_at, last_used_at, expires_at
		 FROM tokens ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	tokens := make([]Token, 0, 4)
	for rows.Next() {
		var t Token
		var created, used, expires string
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.ProjectID, &created, &used,
			&expires); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		if t.ExpiresAt, err = parseStamp(expires); err != nil {
			return nil, err
		}

		if t.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		if t.LastUsedAt, err = time.Parse(time.RFC3339Nano, used); err != nil {
			return nil, fmt.Errorf("parse last_used_at: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (r *repository) count(ctx context.Context) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count tokens: %w", err)
	}
	return count, nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func (r *repository) countInProject(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tokens WHERE project_id = ?`, projectID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count tokens in project: %w", err)
	}
	return count, nil
}
