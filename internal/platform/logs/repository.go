package logs

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

func (r *repository) append(ctx context.Context, instanceID string,
	texts []string, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stamp := at.Format(time.RFC3339Nano)
	for _, text := range texts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO instance_logs (instance_id, text, received_at) VALUES (?, ?, ?)`,
			instanceID, text, stamp); err != nil {
			return fmt.Errorf("append a log line: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM instance_logs WHERE instance_id = ? AND seq <= (
			SELECT seq FROM instance_logs WHERE instance_id = ?
			ORDER BY seq DESC LIMIT 1 OFFSET ?)`,
		instanceID, instanceID, MaxLinesPerInstance); err != nil {
		return fmt.Errorf("trim the log: %w", err)
	}

	return tx.Commit()
}

func (r *repository) tail(ctx context.Context, instanceID string, limit int) ([]Line, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT seq, instance_id, text, received_at FROM instance_logs
			WHERE instance_id = ? ORDER BY seq DESC LIMIT ?`, instanceID, limit)
	if err != nil {
		return nil, fmt.Errorf("read the log: %w", err)
	}
	defer rows.Close()

	lines := make([]Line, 0, limit)
	for rows.Next() {
		var (
			line     Line
			received string
		)
		if err := rows.Scan(&line.Seq, &line.InstanceID, &line.Text, &received); err != nil {
			return nil, fmt.Errorf("scan a log line: %w", err)
		}

		at, err := time.Parse(time.RFC3339Nano, received)
		if err != nil {
			return nil, fmt.Errorf("parse received_at: %w", err)
		}
		line.ReceivedAt = at
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the log: %w", err)
	}

	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines, nil
}

func (r *repository) forget(ctx context.Context, instanceID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM instance_logs WHERE instance_id = ?`, instanceID)
	if err != nil {
		return fmt.Errorf("forget a log: %w", err)
	}
	return nil
}
