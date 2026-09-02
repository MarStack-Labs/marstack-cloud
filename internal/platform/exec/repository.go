package exec

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var errNotFound = errors.New("exec not found")

const columns = `id, project_id, instance_id, node_id, isolation, command,
	timeout_seconds, state,
	output, truncated, exit_code, message, created_at, taken_at, ended_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func stamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

func (r *repository) insert(ctx context.Context, run Run) error {
	command, err := json.Marshal(run.Command)
	if err != nil {
		return fmt.Errorf("encode the command: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO execs (`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ProjectID, run.InstanceID, run.NodeID, run.Isolation, string(command),
		int64(run.Timeout/time.Second), run.State, run.Output, run.Truncated,
		run.ExitCode, run.Message, stamp(run.CreatedAt), stamp(run.TakenAt),
		stamp(run.EndedAt))
	if err != nil {
		return fmt.Errorf("insert a command: %w", err)
	}
	return nil
}

func (r *repository) setTaken(ctx context.Context, id string, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE execs SET state = ?, taken_at = ? WHERE id = ? AND state = ?`,
		StateRunning, stamp(at), id, StateWaiting)
	if err != nil {
		return fmt.Errorf("take a command: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("take a command: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) setDone(ctx context.Context, id, output string, truncated bool,
	exitCode *int, message string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE execs SET state = ?, output = ?, truncated = ?, exit_code = ?,
			message = ?, ended_at = ? WHERE id = ?`,
		StateDone, output, truncated, exitCode, message, stamp(at), id)
	if err != nil {
		return fmt.Errorf("finish a command: %w", err)
	}
	return nil
}

func (r *repository) loseAbandoned(ctx context.Context, before, now time.Time) (int, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE execs SET state = ?, message = ?, ended_at = ?
		 WHERE state IN (?, ?) AND created_at < ?`,
		StateLost, "no node picked this up", stamp(now),
		StateWaiting, StateRunning, stamp(before))
	if err != nil {
		return 0, fmt.Errorf("give up on abandoned commands: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("give up on abandoned commands: %w", err)
	}
	return int(affected), nil
}

func (r *repository) countWaitingIn(ctx context.Context, projectID string) (int, error) {
	var total int
	err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM execs WHERE project_id = ? AND state = ?`,
		projectID, StateWaiting).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count waiting commands: %w", err)
	}
	return total, nil
}

func (r *repository) trim(ctx context.Context, instanceID string, keep int) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM execs WHERE instance_id = ? AND id NOT IN (
			SELECT id FROM execs WHERE instance_id = ?
			ORDER BY created_at DESC, id DESC LIMIT ?)`,
		instanceID, instanceID, keep)
	if err != nil {
		return fmt.Errorf("trim old commands: %w", err)
	}
	return nil
}

func (r *repository) byID(ctx context.Context, id string) (Run, error) {
	found, err := r.load(ctx, `SELECT `+columns+` FROM execs WHERE id = ?`, id)
	if err != nil {
		return Run{}, err
	}
	if len(found) == 0 {
		return Run{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) nextWaitingOn(ctx context.Context, nodeID string) (Run, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM execs WHERE node_id = ? AND state = ?
		 ORDER BY created_at, id LIMIT 1`, nodeID, StateWaiting)
	if err != nil {
		return Run{}, err
	}
	if len(found) == 0 {
		return Run{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) listIn(ctx context.Context, projectID string, limit int) ([]Run, error) {
	return r.load(ctx,
		`SELECT `+columns+` FROM execs WHERE project_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT ?`, projectID, limit)
}

func (r *repository) load(ctx context.Context, query string, args ...any) ([]Run, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list commands: %w", err)
	}
	defer rows.Close()

	runs := make([]Run, 0, 8)
	for rows.Next() {
		var (
			run                   Run
			command               string
			seconds               int64
			created, taken, ended string
		)
		if err := rows.Scan(&run.ID, &run.ProjectID, &run.InstanceID, &run.NodeID,
			&run.Isolation, &command, &seconds, &run.State, &run.Output, &run.Truncated,
			&run.ExitCode, &run.Message, &created, &taken, &ended); err != nil {
			return nil, fmt.Errorf("scan a command: %w", err)
		}

		if err := json.Unmarshal([]byte(command), &run.Command); err != nil {
			return nil, fmt.Errorf("decode the command: %w", err)
		}
		run.Timeout = time.Duration(seconds) * time.Second

		for _, one := range []struct {
			raw  string
			into *time.Time
		}{{created, &run.CreatedAt}, {taken, &run.TakenAt}, {ended, &run.EndedAt}} {
			if one.raw == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, one.raw)
			if err != nil {
				return nil, fmt.Errorf("parse a timestamp: %w", err)
			}
			*one.into = at
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
