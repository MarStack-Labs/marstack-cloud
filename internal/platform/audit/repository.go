package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, entry Entry) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO audit (at, actor, role, project_id, method, path, status, request_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.At.Format(time.RFC3339Nano), entry.Actor, entry.Role, entry.ProjectID,
		entry.Method, entry.Path, entry.Status, entry.RequestID,
	)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

func (r *repository) list(ctx context.Context, limit int, projectID string) ([]Entry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, at, actor, role, project_id, method, path, status, request_id
		 FROM audit WHERE project_id = ? OR project_id = '' ORDER BY id DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, limit)
	for rows.Next() {
		var entry Entry
		var stamp string
		if err := rows.Scan(&entry.ID, &stamp, &entry.Actor, &entry.Role, &entry.ProjectID,
			&entry.Method, &entry.Path, &entry.Status, &entry.RequestID); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		if entry.At, err = time.Parse(time.RFC3339Nano, stamp); err != nil {
			return nil, fmt.Errorf("parse at: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *repository) prune(ctx context.Context, retain int) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM audit WHERE id <= (SELECT MAX(id) - ? FROM audit)`, retain)
	if err != nil {
		return fmt.Errorf("prune audit entries: %w", err)
	}
	return nil
}

func (r *repository) count(ctx context.Context) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count audit entries: %w", err)
	}
	return count, nil
}
