package volume

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
	errNotFound  = errors.New("volume not found")
	errNameTaken = errors.New("volume name already exists")
	errTaken     = errors.New("volume already attached")
)

const columns = `id, name, size_gib, node_id, instance_id, restore_from, created_at, updated_at`

const snapshotColumns = `id, volume_id, name, state, message, size_bytes, created_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, v Volume) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO volumes (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.Name, v.SizeGiB, v.NodeID, v.InstanceID, v.RestoreFrom,
		v.CreatedAt.Format(time.RFC3339Nano), v.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert volume: %w", err)
	}
	return nil
}

func (r *repository) byID(ctx context.Context, id string) (Volume, error) {
	return scanRow(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM volumes WHERE id = ?`, id))
}

func (r *repository) byName(ctx context.Context, name string) (Volume, error) {
	return scanRow(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM volumes WHERE name = ?`, name))
}

func (r *repository) list(ctx context.Context) ([]Volume, error) {
	return r.query(ctx, `SELECT `+columns+` FROM volumes ORDER BY name`)
}

func (r *repository) onNode(ctx context.Context, nodeID string) ([]Volume, error) {
	return r.query(ctx, `SELECT `+columns+` FROM volumes WHERE node_id = ? ORDER BY name`, nodeID)
}

func (r *repository) query(ctx context.Context, sql string, args ...any) ([]Volume, error) {
	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	defer rows.Close()

	volumes := make([]Volume, 0, 8)
	for rows.Next() {
		v, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		volumes = append(volumes, v)
	}
	return volumes, rows.Err()
}

func (r *repository) attach(ctx context.Context, id, instanceID, nodeID string, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE volumes SET instance_id = ?, node_id = ?, updated_at = ?
		 WHERE id = ? AND instance_id = ''`,
		instanceID, nodeID, at.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("attach volume: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("attach volume: %w", err)
	}
	if affected == 0 {
		return errTaken
	}
	return nil
}

func (r *repository) detach(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE volumes SET instance_id = '', updated_at = ? WHERE id = ?`,
		at.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("detach volume: %w", err)
	}
	return nil
}

func (r *repository) detachInstance(ctx context.Context, instanceID string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE volumes SET instance_id = '', updated_at = ? WHERE instance_id = ?`,
		at.Format(time.RFC3339Nano), instanceID,
	)
	if err != nil {
		return fmt.Errorf("detach the volumes of an instance: %w", err)
	}
	return nil
}

func (r *repository) insertSnapshot(ctx context.Context, snap Snapshot) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO snapshots (`+snapshotColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		snap.ID, snap.VolumeID, snap.Name, snap.State, snap.Message, snap.SizeBytes,
		snap.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert snapshot: %w", err)
	}
	return nil
}

func (r *repository) snapshot(ctx context.Context, id string) (Snapshot, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+snapshotColumns+` FROM snapshots WHERE id = ?`, id)

	snap, err := scanSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, errNotFound
	}
	return snap, err
}

func (r *repository) snapshots(ctx context.Context, volumeID string) ([]Snapshot, error) {
	query := `SELECT ` + snapshotColumns + ` FROM snapshots ORDER BY volume_id, name`
	args := []any{}
	if volumeID != "" {
		query = `SELECT ` + snapshotColumns + ` FROM snapshots WHERE volume_id = ? ORDER BY name`
		args = append(args, volumeID)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()

	snapshots := make([]Snapshot, 0, 8)
	for rows.Next() {
		snap, scanErr := scanSnapshot(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

func (r *repository) markSnapshot(ctx context.Context, id, state, message string, size int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE snapshots SET state = ?, message = ?, size_bytes = ? WHERE id = ?`,
		state, message, size, id)
	if err != nil {
		return fmt.Errorf("mark snapshot: %w", err)
	}
	return nil
}

func (r *repository) deleteSnapshot(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) setRestore(ctx context.Context, id, name string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE volumes SET restore_from = ?, updated_at = ? WHERE id = ?`,
		name, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("record the restore: %w", err)
	}
	return nil
}

func scanSnapshot(row scanner) (Snapshot, error) {
	var snap Snapshot
	var created string

	if err := row.Scan(&snap.ID, &snap.VolumeID, &snap.Name, &snap.State, &snap.Message,
		&snap.SizeBytes, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Snapshot{}, err
		}
		return Snapshot{}, fmt.Errorf("scan snapshot: %w", err)
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Snapshot{}, fmt.Errorf("parse created_at: %w", err)
	}
	snap.CreatedAt = at
	return snap, nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE volume_id = ?`, id); err != nil {
		return fmt.Errorf("delete the snapshots of a volume: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM volumes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete volume: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete volume: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return tx.Commit()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(row *sql.Row) (Volume, error) {
	v, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Volume{}, errNotFound
	}
	return v, err
}

func scan(row scanner) (Volume, error) {
	var v Volume
	var created, updated string

	if err := row.Scan(&v.ID, &v.Name, &v.SizeGiB, &v.NodeID, &v.InstanceID, &v.RestoreFrom,
		&created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Volume{}, err
		}
		return Volume{}, fmt.Errorf("scan volume: %w", err)
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Volume{}, fmt.Errorf("parse created_at: %w", err)
	}
	v.CreatedAt = at

	at, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Volume{}, fmt.Errorf("parse updated_at: %w", err)
	}
	v.UpdatedAt = at

	return v, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
