package event

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

const columns = `id, at, project_id, kind, subject, node_id, message, severity`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, entry Entry) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO events (at, project_id, kind, subject, node_id, message, severity)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.At.Format(time.RFC3339Nano), entry.ProjectID, entry.Kind, entry.Subject,
		entry.NodeID, entry.Message, entry.Severity,
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

func (r *repository) list(ctx context.Context, filter Filter) ([]Entry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+columns+` FROM events
			WHERE project_id = ?
				AND (? = '' OR subject = ?)
				AND (? = '' OR kind = ?)
				AND (? = '' OR severity = ?)
			ORDER BY id DESC LIMIT ?`,
		filter.ProjectID,
		filter.Subject, filter.Subject,
		filter.Kind, filter.Kind,
		filter.Severity, filter.Severity,
		filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, filter.Limit)
	for rows.Next() {
		var (
			entry Entry
			at    string
		)
		if err := rows.Scan(&entry.ID, &at, &entry.ProjectID, &entry.Kind, &entry.Subject,
			&entry.NodeID, &entry.Message, &entry.Severity); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}

		when, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("parse at: %w", err)
		}
		entry.At = when
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *repository) since(ctx context.Context, afterID int64, limit int) ([]Entry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+columns+` FROM events WHERE id > ? ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("read events after %d: %w", afterID, err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, limit)
	for rows.Next() {
		var (
			entry Entry
			at    string
		)
		if err := rows.Scan(&entry.ID, &at, &entry.ProjectID, &entry.Kind, &entry.Subject,
			&entry.NodeID, &entry.Message, &entry.Severity); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}

		when, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("parse at: %w", err)
		}
		entry.At = when
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *repository) newestID(ctx context.Context) (int64, error) {
	var newest sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&newest); err != nil {
		return 0, fmt.Errorf("read the newest event id: %w", err)
	}
	return newest.Int64, nil
}

func (r *repository) prune(ctx context.Context, retain int) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM events WHERE id <= (SELECT MAX(id) - ? FROM events)`, retain)
	if err != nil {
		return fmt.Errorf("prune events: %w", err)
	}
	return nil
}

func (r *repository) count(ctx context.Context) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return count, nil
}
