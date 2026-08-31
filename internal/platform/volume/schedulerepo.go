package volume

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const scheduleColumns = `id, project_id, volume_id, every_seconds, keep,
	next_at, last_at, created_at, updated_at`

func (r *repository) upsertSchedule(ctx context.Context, sc Schedule) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO snapshot_schedules (`+scheduleColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (volume_id) DO UPDATE SET
				every_seconds = excluded.every_seconds,
				keep          = excluded.keep,
				next_at       = excluded.next_at,
				updated_at    = excluded.updated_at`,
		sc.ID, sc.ProjectID, sc.VolumeID, int(sc.Every.Seconds()), sc.Keep,
		sc.NextAt.Format(time.RFC3339Nano), stamp(sc.LastAt),
		sc.CreatedAt.Format(time.RFC3339Nano), sc.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert snapshot schedule: %w", err)
	}
	return nil
}

func stamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

func (r *repository) scheduleOf(ctx context.Context, volumeID string) (Schedule, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM snapshot_schedules WHERE volume_id = ?`, volumeID)

	sc, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, errNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("read snapshot schedule: %w", err)
	}
	return sc, nil
}

func (r *repository) deleteSchedule(ctx context.Context, volumeID string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM snapshot_schedules WHERE volume_id = ?`, volumeID); err != nil {
		return fmt.Errorf("delete snapshot schedule: %w", err)
	}
	return nil
}

func (r *repository) schedulesIn(ctx context.Context, projectID string) ([]Schedule, error) {
	return r.querySchedules(ctx,
		`SELECT `+scheduleColumns+` FROM snapshot_schedules
			WHERE project_id = ? ORDER BY created_at`, projectID)
}

func (r *repository) schedulesDue(ctx context.Context, now time.Time) ([]Schedule, error) {
	return r.querySchedules(ctx,
		`SELECT `+scheduleColumns+` FROM snapshot_schedules
			WHERE next_at <= ? ORDER BY next_at`, now.Format(time.RFC3339Nano))
}

func (r *repository) querySchedules(
	ctx context.Context, query string, args ...any,
) ([]Schedule, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list snapshot schedules: %w", err)
	}
	defer rows.Close()

	schedules := make([]Schedule, 0)
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, sc)
	}
	return schedules, rows.Err()
}

func (r *repository) markScheduleFired(ctx context.Context, id string, at, next time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE snapshot_schedules SET last_at = ?, next_at = ?, updated_at = ? WHERE id = ?`,
		at.Format(time.RFC3339Nano), next.Format(time.RFC3339Nano),
		at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("mark snapshot schedule fired: %w", err)
	}
	return nil
}

func (r *repository) pendingSnapshotsForVolume(ctx context.Context, volumeID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM snapshots WHERE volume_id = ? AND state = ?`,
		volumeID, SnapshotPending,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending snapshots: %w", err)
	}
	return count, nil
}

func (r *repository) snapshotsBySchedule(
	ctx context.Context, scheduleID string,
) ([]Snapshot, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+snapshotColumns+` FROM snapshots
			WHERE schedule_id = ? ORDER BY created_at DESC`, scheduleID)
	if err != nil {
		return nil, fmt.Errorf("list snapshots made by a schedule: %w", err)
	}
	defer rows.Close()

	snapshots := make([]Snapshot, 0)
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

func scanSchedule(row scanner) (Schedule, error) {
	var (
		sc      Schedule
		seconds int
		next    string
		last    string
		created string
		updated string
	)

	if err := row.Scan(&sc.ID, &sc.ProjectID, &sc.VolumeID, &seconds, &sc.Keep,
		&next, &last, &created, &updated); err != nil {
		return Schedule{}, err
	}
	sc.Every = time.Duration(seconds) * time.Second

	for _, pair := range []struct {
		raw  string
		into *time.Time
	}{{next, &sc.NextAt}, {last, &sc.LastAt}, {created, &sc.CreatedAt}, {updated, &sc.UpdatedAt}} {
		if pair.raw == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, pair.raw)
		if err != nil {
			return Schedule{}, fmt.Errorf("parse snapshot schedule timestamp: %w", err)
		}
		*pair.into = at
	}
	return sc, nil
}
