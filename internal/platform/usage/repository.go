package usage

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

func (r *repository) replace(ctx context.Context, report Report) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stamp := report.Node.ReportedAt.Format(time.RFC3339Nano)

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO node_usage (node_id, cpu_percent, memory_used_mib, memory_mib, reported_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (node_id) DO UPDATE SET
		   cpu_percent = excluded.cpu_percent,
		   memory_used_mib = excluded.memory_used_mib,
		   memory_mib = excluded.memory_mib,
		   reported_at = excluded.reported_at`,
		report.Node.NodeID, report.Node.CPUPercent, report.Node.MemoryUsedMiB,
		report.Node.MemoryMiB, stamp,
	); err != nil {
		return fmt.Errorf("record node usage: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM instance_usage WHERE node_id = ?`, report.Node.NodeID); err != nil {
		return fmt.Errorf("clear instance usage: %w", err)
	}

	for _, sample := range report.Instances {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO instance_usage
			   (instance_id, node_id, cpu_percent, memory_used_mib, reported_at)
			 VALUES (?, ?, ?, ?, ?)`,
			sample.InstanceID, report.Node.NodeID, sample.CPUPercent, sample.MemoryUsedMiB, stamp,
		); err != nil {
			return fmt.Errorf("record instance usage: %w", err)
		}
	}

	return tx.Commit()
}

func (r *repository) nodes(ctx context.Context) ([]NodeSample, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT node_id, cpu_percent, memory_used_mib, memory_mib, reported_at
		 FROM node_usage ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("list node usage: %w", err)
	}
	defer rows.Close()

	samples := make([]NodeSample, 0, 4)
	for rows.Next() {
		var sample NodeSample
		var stamp string
		if err := rows.Scan(&sample.NodeID, &sample.CPUPercent, &sample.MemoryUsedMiB,
			&sample.MemoryMiB, &stamp); err != nil {
			return nil, fmt.Errorf("scan node usage: %w", err)
		}
		if sample.ReportedAt, err = time.Parse(time.RFC3339Nano, stamp); err != nil {
			return nil, fmt.Errorf("parse reported_at: %w", err)
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

func (r *repository) instances(ctx context.Context) ([]InstanceSample, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT instance_id, node_id, cpu_percent, memory_used_mib, reported_at
		 FROM instance_usage ORDER BY instance_id`)
	if err != nil {
		return nil, fmt.Errorf("list instance usage: %w", err)
	}
	defer rows.Close()

	samples := make([]InstanceSample, 0, 8)
	for rows.Next() {
		var sample InstanceSample
		var stamp string
		if err := rows.Scan(&sample.InstanceID, &sample.NodeID, &sample.CPUPercent,
			&sample.MemoryUsedMiB, &stamp); err != nil {
			return nil, fmt.Errorf("scan instance usage: %w", err)
		}
		if sample.ReportedAt, err = time.Parse(time.RFC3339Nano, stamp); err != nil {
			return nil, fmt.Errorf("parse reported_at: %w", err)
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

func (r *repository) forget(ctx context.Context, nodeID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM node_usage WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("forget node usage: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM instance_usage WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("forget instance usage: %w", err)
	}
	return nil
}
