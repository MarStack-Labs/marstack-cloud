package forward

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
	errNotFound = errors.New("forward not found")
	errPortUsed = errors.New("node port already published")
)

const columns = `id, project_id, instance_id, protocol, node_port, target_port, node_id, address, family,
	tls_material, tls_key_id, tls_subject, tls_expires_at, created_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, f Forward) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO forwards (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.ProjectID, f.InstanceID, f.Protocol, f.NodePort, f.TargetPort, f.NodeID,
		f.Address, f.Family,
		f.TLS.Material, f.TLS.KeyID, f.TLS.Subject, f.TLS.ExpiresAt,
		f.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errPortUsed
		}
		return fmt.Errorf("insert forward: %w", err)
	}
	return nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Forward, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM forwards WHERE project_id = ? ORDER BY node_port`, projectID)
}

func (r *repository) byID(ctx context.Context, id string) (Forward, error) {
	found, err := r.query(ctx, `SELECT `+columns+` FROM forwards WHERE id = ?`, id)
	if err != nil {
		return Forward{}, err
	}
	if len(found) == 0 {
		return Forward{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) onNode(ctx context.Context, nodeID string) ([]Forward, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM forwards WHERE node_id = ? ORDER BY node_port`, nodeID)
}

func (r *repository) portTaken(ctx context.Context, protocol string, port int) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forwards WHERE protocol = ? AND node_port = ?`,
		protocol, port).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check the node port: %w", err)
	}
	return count > 0, nil
}

func (r *repository) query(ctx context.Context, sql string, args ...any) ([]Forward, error) {
	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list forwards: %w", err)
	}
	defer rows.Close()

	forwards := make([]Forward, 0, 8)
	for rows.Next() {
		f, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		forwards = append(forwards, f)
	}
	return forwards, rows.Err()
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM forwards WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete forward: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete forward: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) deleteInstance(ctx context.Context, instanceID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM forwards WHERE instance_id = ?`, instanceID)
	if err != nil {
		return fmt.Errorf("delete the forwards of an instance: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Forward, error) {
	var f Forward
	var created string

	if err := row.Scan(&f.ID, &f.ProjectID, &f.InstanceID, &f.Protocol, &f.NodePort, &f.TargetPort,
		&f.NodeID, &f.Address, &f.Family,
		&f.TLS.Material, &f.TLS.KeyID, &f.TLS.Subject, &f.TLS.ExpiresAt,
		&created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Forward{}, err
		}
		return Forward{}, fmt.Errorf("scan forward: %w", err)
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Forward{}, fmt.Errorf("parse created_at: %w", err)
	}
	f.CreatedAt = at
	return f, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func (r *repository) setTLS(ctx context.Context, id string, t TLS) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE forwards SET tls_material = ?, tls_key_id = ?, tls_subject = ?,
			tls_expires_at = ? WHERE id = ?`,
		t.Material, t.KeyID, t.Subject, t.ExpiresAt, id)
	if err != nil {
		return fmt.Errorf("set the forward certificate: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set the forward certificate: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}
