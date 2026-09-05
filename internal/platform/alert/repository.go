package alert

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
	errNotFound  = errors.New("alert not found")
	errNameTaken = errors.New("alert name already used")
)

const columns = `id, project_id, name, instance_id, metric, comparison, threshold, ` +
	`for_seconds, state, since, last_value, message, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, a Alert) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO alerts (`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.Name, a.InstanceID, a.Metric, a.Comparison, a.Threshold,
		int(a.For.Seconds()), a.State, a.Since.Format(time.RFC3339Nano), a.LastValue,
		a.Message, a.CreatedAt.Format(time.RFC3339Nano),
		a.UpdatedAt.Format(time.RFC3339Nano))

	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errNameTaken
	}
	if err != nil {
		return fmt.Errorf("insert an alert: %w", err)
	}
	return nil
}

func (r *repository) setState(ctx context.Context, a Alert) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE alerts SET state = ?, since = ?, last_value = ?, message = ?, updated_at = ?
			WHERE id = ?`,
		a.State, a.Since.Format(time.RFC3339Nano), a.LastValue, a.Message,
		a.UpdatedAt.Format(time.RFC3339Nano), a.ID)
	if err != nil {
		return fmt.Errorf("update an alert: %w", err)
	}
	return nil
}

func (r *repository) all(ctx context.Context) ([]Alert, error) {
	return r.query(ctx, `SELECT `+columns+` FROM alerts ORDER BY created_at`)
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Alert, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM alerts WHERE project_id = ? ORDER BY created_at`, projectID)
}

func (r *repository) countIn(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alerts WHERE project_id = ?`, projectID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count alerts: %w", err)
	}
	return count, nil
}

func (r *repository) find(ctx context.Context, projectID, ref string) (Alert, error) {
	held, err := r.query(ctx,
		`SELECT `+columns+` FROM alerts WHERE project_id = ? AND (id = ? OR name = ?)`,
		projectID, ref, ref)
	if err != nil {
		return Alert{}, err
	}
	if len(held) == 0 {
		return Alert{}, errNotFound
	}
	return held[0], nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM alerts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete an alert: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete an alert: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) deleteInstance(ctx context.Context, instanceID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM alerts WHERE instance_id = ?`, instanceID)
	if err != nil {
		return fmt.Errorf("delete the alerts of an instance: %w", err)
	}
	return nil
}

func (r *repository) query(ctx context.Context, sql string, args ...any) ([]Alert, error) {
	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()

	held := make([]Alert, 0, 8)
	for rows.Next() {
		var a Alert
		var seconds int
		var since, created, updated string

		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Name, &a.InstanceID, &a.Metric,
			&a.Comparison, &a.Threshold, &seconds, &a.State, &since, &a.LastValue,
			&a.Message, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan an alert: %w", err)
		}

		a.For = time.Duration(seconds) * time.Second
		a.Since, _ = time.Parse(time.RFC3339Nano, since)
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		held = append(held, a)
	}
	return held, rows.Err()
}
