package quota

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var errNotFound = errors.New("no quota set")

const columns = `project_id, instances, vcpu, memory_mib, volumes, volume_gib, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) upsert(ctx context.Context, q Quota) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO quotas (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (project_id) DO UPDATE SET
		   instances = excluded.instances,
		   vcpu = excluded.vcpu,
		   memory_mib = excluded.memory_mib,
		   volumes = excluded.volumes,
		   volume_gib = excluded.volume_gib,
		   updated_at = excluded.updated_at`,
		q.ProjectID, q.Limits.Instances, q.Limits.VCPU, q.Limits.MemoryMiB,
		q.Limits.Volumes, q.Limits.VolumeGiB, q.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("write quota: %w", err)
	}
	return nil
}

func (r *repository) byProject(ctx context.Context, projectID string) (Quota, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM quotas WHERE project_id = ?`, projectID)

	q, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Quota{}, errNotFound
	}
	if err != nil {
		return Quota{}, fmt.Errorf("read quota: %w", err)
	}
	return q, nil
}

func (r *repository) list(ctx context.Context) ([]Quota, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM quotas ORDER BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("list quotas: %w", err)
	}
	defer rows.Close()

	quotas := make([]Quota, 0, 4)
	for rows.Next() {
		q, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		quotas = append(quotas, q)
	}
	return quotas, rows.Err()
}

func (r *repository) delete(ctx context.Context, projectID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM quotas WHERE project_id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("delete quota: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete quota: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Quota, error) {
	var (
		q       Quota
		updated string
	)
	if err := row.Scan(&q.ProjectID, &q.Limits.Instances, &q.Limits.VCPU, &q.Limits.MemoryMiB,
		&q.Limits.Volumes, &q.Limits.VolumeGiB, &updated); err != nil {
		return Quota{}, err
	}

	at, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Quota{}, fmt.Errorf("parse updated_at: %w", err)
	}
	q.UpdatedAt = at
	return q, nil
}
