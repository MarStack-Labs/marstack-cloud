package registry

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
	errNotFound  = errors.New("credential not found")
	errHostTaken = errors.New("host already has a credential")
)

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, c Credential) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO registry_credentials (id, host, username, sealed, key_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.Host, c.Username, c.Sealed, c.KeyID,
		c.CreatedAt.Format(time.RFC3339Nano))

	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errHostTaken
	}
	if err != nil {
		return fmt.Errorf("insert a registry credential: %w", err)
	}
	return nil
}

func (r *repository) all(ctx context.Context) ([]Credential, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, host, username, sealed, key_id, created_at
			FROM registry_credentials ORDER BY host`)
	if err != nil {
		return nil, fmt.Errorf("list registry credentials: %w", err)
	}
	defer rows.Close()

	held := make([]Credential, 0, 4)
	for rows.Next() {
		var c Credential
		var created string
		if err := rows.Scan(&c.ID, &c.Host, &c.Username, &c.Sealed, &c.KeyID,
			&created); err != nil {
			return nil, fmt.Errorf("scan a registry credential: %w", err)
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		held = append(held, c)
	}
	return held, rows.Err()
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM registry_credentials WHERE id = ? OR host = ?`, id, id)
	if err != nil {
		return fmt.Errorf("delete a registry credential: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete a registry credential: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}
