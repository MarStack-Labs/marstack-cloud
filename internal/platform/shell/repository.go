package shell

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var errNotFound = errors.New("session not found")

const columns = `id, project_id, instance_id, node_id, isolation, command, state, ` +
	`message, created_at, taken_at, ended_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, s Session) error {
	command, err := json.Marshal(s.Command)
	if err != nil {
		return fmt.Errorf("encode the command: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO shell_sessions (`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, s.InstanceID, s.NodeID, s.Isolation, string(command),
		s.State, s.Message, s.CreatedAt.Format(time.RFC3339Nano),
		s.TakenAt.Format(time.RFC3339Nano), s.EndedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert a session: %w", err)
	}
	return nil
}

func (r *repository) byID(ctx context.Context, id string) (Session, error) {
	held, err := r.query(ctx, `SELECT `+columns+` FROM shell_sessions WHERE id = ?`, id)
	if err != nil {
		return Session{}, err
	}
	if len(held) == 0 {
		return Session{}, errNotFound
	}
	return held[0], nil
}

func (r *repository) nextWaitingOn(ctx context.Context, nodeID string) (Session, error) {
	held, err := r.query(ctx,
		`SELECT `+columns+` FROM shell_sessions
			WHERE node_id = ? AND state = ? ORDER BY created_at LIMIT 1`,
		nodeID, StateWaiting)
	if err != nil {
		return Session{}, err
	}
	if len(held) == 0 {
		return Session{}, errNotFound
	}
	return held[0], nil
}

func (r *repository) take(ctx context.Context, id string, at time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE shell_sessions SET state = ?, taken_at = ?
			WHERE id = ? AND state = ?`,
		StateAttached, at.Format(time.RFC3339Nano), id, StateWaiting)
	if err != nil {
		return false, fmt.Errorf("take a session: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("take a session: %w", err)
	}
	return affected == 1, nil
}

func (r *repository) close(ctx context.Context, id, message string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE shell_sessions SET state = ?, message = ?, ended_at = ?
			WHERE id = ? AND state != ?`,
		StateClosed, message, at.Format(time.RFC3339Nano), id, StateClosed)
	if err != nil {
		return fmt.Errorf("close a session: %w", err)
	}
	return nil
}

func (r *repository) countLiveIn(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM shell_sessions WHERE project_id = ? AND state != ?`,
		projectID, StateClosed).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count sessions: %w", err)
	}
	return count, nil
}

func (r *repository) listIn(ctx context.Context, projectID string, limit int) ([]Session, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM shell_sessions WHERE project_id = ?
			ORDER BY created_at DESC LIMIT ?`, projectID, limit)
}

func (r *repository) stale(ctx context.Context, before time.Time) ([]Session, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM shell_sessions WHERE state = ? AND created_at < ?`,
		StateWaiting, before.Format(time.RFC3339Nano))
}

func (r *repository) deleteInstance(ctx context.Context, instanceID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM shell_sessions WHERE instance_id = ?`, instanceID)
	if err != nil {
		return fmt.Errorf("delete the sessions of an instance: %w", err)
	}
	return nil
}

func (r *repository) query(ctx context.Context, sql string, args ...any) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	held := make([]Session, 0, 4)
	for rows.Next() {
		var s Session
		var command, created, taken, ended string

		if err := rows.Scan(&s.ID, &s.ProjectID, &s.InstanceID, &s.NodeID, &s.Isolation,
			&command, &s.State, &s.Message, &created, &taken, &ended); err != nil {
			return nil, fmt.Errorf("scan a session: %w", err)
		}

		_ = json.Unmarshal([]byte(command), &s.Command)
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		s.TakenAt, _ = time.Parse(time.RFC3339Nano, taken)
		s.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
		held = append(held, s)
	}
	return held, rows.Err()
}
