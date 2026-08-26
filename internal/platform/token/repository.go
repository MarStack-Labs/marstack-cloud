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

const columns = `id, name, role, secret_hash, created_at, last_used_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, t Token, hash string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tokens (`+columns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Role, hash,
		t.CreatedAt.Format(time.RFC3339Nano), t.LastUsedAt.Format(time.RFC3339Nano),
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
	var identity Identity
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, role FROM tokens WHERE secret_hash = ?`, hash,
	).Scan(&identity.ID, &identity.Name, &identity.Role)

	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, errNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("look up token: %w", err)
	}
	return identity, nil
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
		`SELECT id, name, role, created_at, last_used_at FROM tokens ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	tokens := make([]Token, 0, 4)
	for rows.Next() {
		var t Token
		var created, used string
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &created, &used); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
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

func (r *repository) countByRole(ctx context.Context, role string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tokens WHERE role = ?`, role).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count tokens by role: %w", err)
	}
	return count, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
