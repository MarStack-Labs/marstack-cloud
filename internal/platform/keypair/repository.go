package keypair

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
	errNotFound  = errors.New("key not found")
	errNameTaken = errors.New("key name already exists")
)

const columns = `id, project_id, name, public_key, fingerprint, kind, comment, created_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, k Key) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO ssh_keys (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.ProjectID, k.Name, k.PublicKey, k.Fingerprint, k.Kind, k.Comment,
		k.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert key: %w", err)
	}
	return nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Key, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM ssh_keys WHERE project_id = ? ORDER BY name`, projectID)
}

func (r *repository) byName(ctx context.Context, projectID, name string) (Key, error) {
	found, err := r.query(ctx,
		`SELECT `+columns+` FROM ssh_keys WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return Key{}, err
	}
	if len(found) == 0 {
		return Key{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) byID(ctx context.Context, projectID, id string) (Key, error) {
	found, err := r.query(ctx,
		`SELECT `+columns+` FROM ssh_keys WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return Key{}, err
	}
	if len(found) == 0 {
		return Key{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) query(ctx context.Context, sql string, args ...any) ([]Key, error) {
	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()

	keys := make([]Key, 0, 4)
	for rows.Next() {
		var (
			k       Key
			created string
		)
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Name, &k.PublicKey, &k.Fingerprint,
			&k.Kind, &k.Comment, &created); err != nil {
			return nil, fmt.Errorf("scan key: %w", err)
		}

		at, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		k.CreatedAt = at
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (r *repository) delete(ctx context.Context, projectID, id string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM ssh_keys WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete key: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete key: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
