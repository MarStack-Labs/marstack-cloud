package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/page"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var (
	errNotFound  = errors.New("backup not found")
	errNameTaken = errors.New("backup name already exists")
)

const columns = `id, project_id, schedule_id, volume_id, node_id, name, state, message, ` +
	`size_bytes, checksum, key_id, volume_key_sealed, volume_key_id, content_key, ` +
	`created_at, updated_at`

const scheduleColumns = `id, project_id, volume_id, every_seconds, keep, next_at, last_at, ` +
	`created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, b Backup) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO backups (`+columns+`) VALUES `+
			`(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ProjectID, b.ScheduleID, b.VolumeID, b.NodeID, b.Name, b.State, b.Message,
		b.SizeBytes, b.Checksum, b.KeyID, b.VolumeKeySealed, b.VolumeKeyID, b.ContentKey,
		b.CreatedAt.Format(time.RFC3339Nano), b.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert backup: %w", err)
	}
	return nil
}

func (r *repository) byID(ctx context.Context, id string) (Backup, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM backups WHERE id = ?`, id)

	b, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Backup{}, errNotFound
	}
	if err != nil {
		return Backup{}, fmt.Errorf("read backup: %w", err)
	}
	return b, nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Backup, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM backups WHERE project_id = ? ORDER BY created_at DESC`, projectID)
}

func (r *repository) pageIn(
	ctx context.Context, projectID string, window page.Window,
) ([]Backup, error) {
	after := window.After
	return r.query(ctx,
		`SELECT `+columns+` FROM backups
			WHERE project_id = ?
				AND (? = '' OR created_at < ? OR (created_at = ? AND id < ?))
			ORDER BY created_at DESC, id DESC LIMIT ?`,
		projectID, after.Order, after.Order, after.Order, after.ID, window.Limit)
}

func (r *repository) listForVolume(ctx context.Context, volumeID string) ([]Backup, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM backups WHERE volume_id = ? ORDER BY created_at DESC`, volumeID)
}

func (r *repository) pendingOn(ctx context.Context, nodeID string) ([]Backup, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM backups WHERE node_id = ? AND state = ? ORDER BY created_at`,
		nodeID, StatePending)
}

func (r *repository) query(ctx context.Context, sql string, args ...any) ([]Backup, error) {
	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()

	backups := make([]Backup, 0, 8)
	for rows.Next() {
		b, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		backups = append(backups, b)
	}
	return backups, rows.Err()
}

func (r *repository) mark(ctx context.Context, id, state, message string,
	size int64, checksum, keyID string, at time.Time,
) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE backups SET state = ?, message = ?, size_bytes = ?, checksum = ?, key_id = ?,
		 updated_at = ? WHERE id = ?`,
		state, message, size, checksum, keyID, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("mark backup: %w", err)
	}
	return nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Backup, error) {
	var (
		b                Backup
		created, updated string
	)
	if err := row.Scan(&b.ID, &b.ProjectID, &b.ScheduleID, &b.VolumeID, &b.NodeID, &b.Name,
		&b.State, &b.Message, &b.SizeBytes, &b.Checksum, &b.KeyID,
		&b.VolumeKeySealed, &b.VolumeKeyID, &b.ContentKey, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Backup{}, err
		}
		return Backup{}, fmt.Errorf("scan backup: %w", err)
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Backup{}, fmt.Errorf("parse created_at: %w", err)
	}
	b.CreatedAt = at

	at, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Backup{}, fmt.Errorf("parse updated_at: %w", err)
	}
	b.UpdatedAt = at
	return b, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func (r *repository) upsertSchedule(ctx context.Context, sc Schedule) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO backup_schedules (`+scheduleColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (volume_id) DO UPDATE SET
		   every_seconds = excluded.every_seconds,
		   keep = excluded.keep,
		   next_at = excluded.next_at,
		   updated_at = excluded.updated_at`,
		sc.ID, sc.ProjectID, sc.VolumeID, int64(sc.Every.Seconds()), sc.Keep,
		sc.NextAt.Format(time.RFC3339Nano), stampOf(sc.LastAt),
		sc.CreatedAt.Format(time.RFC3339Nano), sc.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("write backup schedule: %w", err)
	}
	return nil
}

func (r *repository) scheduleOf(ctx context.Context, volumeID string) (Schedule, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM backup_schedules WHERE volume_id = ?`, volumeID)

	sc, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, errNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("read backup schedule: %w", err)
	}
	return sc, nil
}

func (r *repository) scheduleByID(ctx context.Context, id string) (Schedule, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM backup_schedules WHERE id = ?`, id)

	sc, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, errNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("read backup schedule: %w", err)
	}
	return sc, nil
}

func (r *repository) schedulesIn(ctx context.Context, projectID string) ([]Schedule, error) {
	return r.querySchedules(ctx,
		`SELECT `+scheduleColumns+` FROM backup_schedules WHERE project_id = ? ORDER BY next_at`,
		projectID)
}

func (r *repository) schedulesDue(ctx context.Context, at time.Time) ([]Schedule, error) {
	return r.querySchedules(ctx,
		`SELECT `+scheduleColumns+` FROM backup_schedules WHERE next_at <= ? ORDER BY next_at`,
		at.Format(time.RFC3339Nano))
}

func (r *repository) querySchedules(ctx context.Context, query string, args ...any) ([]Schedule, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list backup schedules: %w", err)
	}
	defer rows.Close()

	schedules := make([]Schedule, 0, 4)
	for rows.Next() {
		sc, scanErr := scanSchedule(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		schedules = append(schedules, sc)
	}
	return schedules, rows.Err()
}

func (r *repository) markScheduleFired(ctx context.Context, id string, last, next time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE backup_schedules SET last_at = ?, next_at = ?, updated_at = ? WHERE id = ?`,
		last.Format(time.RFC3339Nano), next.Format(time.RFC3339Nano),
		last.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("record the schedule firing: %w", err)
	}
	return nil
}

func (r *repository) deleteSchedule(ctx context.Context, volumeID string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM backup_schedules WHERE volume_id = ?`, volumeID)
	if err != nil {
		return fmt.Errorf("delete backup schedule: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete backup schedule: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) madeBySchedule(ctx context.Context, scheduleID string) ([]Backup, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM backups WHERE schedule_id = ? ORDER BY created_at DESC`,
		scheduleID)
}

func (r *repository) pendingForVolume(ctx context.Context, volumeID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM backups WHERE volume_id = ? AND state = ?`,
		volumeID, StatePending).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending backups: %w", err)
	}
	return count, nil
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
		return time.Time{}, fmt.Errorf("parse a timestamp: %w", err)
	}
	return at, nil
}

func scanSchedule(row scanner) (Schedule, error) {
	var (
		sc      Schedule
		seconds int64
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

	stamps := []struct {
		raw    string
		target *time.Time
	}{
		{next, &sc.NextAt}, {last, &sc.LastAt}, {created, &sc.CreatedAt}, {updated, &sc.UpdatedAt},
	}
	for _, stamp := range stamps {
		at, err := parseStamp(stamp.raw)
		if err != nil {
			return Schedule{}, err
		}
		*stamp.target = at
	}
	return sc, nil
}
