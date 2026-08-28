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

func (r *repository) accumulate(ctx context.Context, bucket int64, samples []Bucket, subjects []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	for i, subject := range subjects {
		sample := samples[i]
		_, err := tx.ExecContext(ctx,
			`INSERT INTO usage_history
				(subject, bucket, samples, cpu_sum, cpu_peak, memory_sum, memory_peak, memory_mib)
				VALUES (?, ?, 1, ?, ?, ?, ?, ?)
				ON CONFLICT (subject, bucket) DO UPDATE SET
					samples = samples + 1,
					cpu_sum = cpu_sum + excluded.cpu_sum,
					cpu_peak = MAX(cpu_peak, excluded.cpu_peak),
					memory_sum = memory_sum + excluded.memory_sum,
					memory_peak = MAX(memory_peak, excluded.memory_peak),
					memory_mib = excluded.memory_mib`,
			subject, bucket, sample.CPUPeak, sample.CPUPeak,
			sample.MemoryPeak, sample.MemoryPeak, sample.MemoryMiB)
		if err != nil {
			return fmt.Errorf("accumulate usage: %w", err)
		}
	}
	return tx.Commit()
}

func (r *repository) history(ctx context.Context, subject string, from int64) ([]Bucket, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT bucket, samples, cpu_sum, cpu_peak, memory_sum, memory_peak, memory_mib
			FROM usage_history WHERE subject = ? AND bucket >= ? ORDER BY bucket`,
		subject, from)
	if err != nil {
		return nil, fmt.Errorf("read usage history: %w", err)
	}
	defer rows.Close()

	buckets := make([]Bucket, 0, 64)
	for rows.Next() {
		var (
			at        int64
			samples   int
			cpuSum    float64
			cpuPeak   float64
			memorySum int
		)
		var bucket Bucket
		if err := rows.Scan(&at, &samples, &cpuSum, &cpuPeak, &memorySum,
			&bucket.MemoryPeak, &bucket.MemoryMiB); err != nil {
			return nil, fmt.Errorf("scan a bucket: %w", err)
		}

		bucket.At = time.Unix(at*int64(BucketSize/time.Second), 0).UTC()
		bucket.Samples = samples
		bucket.CPUPeak = cpuPeak
		if samples > 0 {
			bucket.CPUAverage = cpuSum / float64(samples)
			bucket.MemoryAverage = memorySum / samples
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (r *repository) pruneHistory(ctx context.Context, before int64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM usage_history WHERE bucket < ?`, before)
	if err != nil {
		return fmt.Errorf("prune usage history: %w", err)
	}
	return nil
}
