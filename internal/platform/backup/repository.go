package backup

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
	errNotFound  = errors.New("backup not found")
	errNameTaken = errors.New("backup name already exists")
)

const columns = `id, project_id, volume_id, node_id, name, state, message, size_bytes, ` +
	`checksum, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, b Backup) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO backups (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ProjectID, b.VolumeID, b.NodeID, b.Name, b.State, b.Message, b.SizeBytes,
		b.Checksum, b.CreatedAt.Format(time.RFC3339Nano), b.UpdatedAt.Format(time.RFC3339Nano),
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
	size int64, checksum string, at time.Time,
) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE backups SET state = ?, message = ?, size_bytes = ?, checksum = ?, updated_at = ?
		 WHERE id = ?`,
		state, message, size, checksum, at.Format(time.RFC3339Nano), id)
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
	if err := row.Scan(&b.ID, &b.ProjectID, &b.VolumeID, &b.NodeID, &b.Name, &b.State,
		&b.Message, &b.SizeBytes, &b.Checksum, &created, &updated); err != nil {
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
