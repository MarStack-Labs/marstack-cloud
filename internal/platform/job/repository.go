package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var (
	errNotFound = errors.New("job not found")
	errNameUsed = errors.New("job name already used")
)

const columns = `id, project_id, name, template, env, files, seal_key_id, every_seconds,
	retries, keep_runs, paused, next_at, last_at, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, j Job) error {
	template, err := json.Marshal(j.Template)
	if err != nil {
		return fmt.Errorf("encode the template: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO jobs (`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.ProjectID, j.Name, string(template), j.EnvSealed, j.FilesSealed, j.SealKeyID,
		int64(j.Every/time.Second), j.Retries, j.Keep, j.Paused,
		stamp(j.NextAt), stamp(j.LastAt),
		j.CreatedAt.Format(time.RFC3339Nano), j.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueViolation(err) {
			return errNameUsed
		}
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

func stamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

func (r *repository) setPaused(ctx context.Context, id string, paused bool, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE jobs SET paused = ?, updated_at = ? WHERE id = ?`,
		paused, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set paused: %w", err)
	}
	return oneRow(result)
}

func (r *repository) setNextAt(ctx context.Context, id string, next, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE jobs SET next_at = ?, last_at = ?, updated_at = ? WHERE id = ?`,
		stamp(next), stamp(at), at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set the next run: %w", err)
	}
	return nil
}

func oneRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count affected rows: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Job, error) {
	return r.load(ctx,
		`SELECT `+columns+` FROM jobs WHERE project_id = ? ORDER BY name`, projectID)
}

func (r *repository) all(ctx context.Context) ([]Job, error) {
	return r.load(ctx, `SELECT `+columns+` FROM jobs ORDER BY id`)
}

func (r *repository) due(ctx context.Context, now time.Time, limit int) ([]Job, error) {
	return r.load(ctx,
		`SELECT `+columns+` FROM jobs
			WHERE paused = 0 AND every_seconds > 0 AND next_at != '' AND next_at <= ?
			ORDER BY next_at LIMIT ?`,
		now.Format(time.RFC3339Nano), limit)
}

func (r *repository) byID(ctx context.Context, projectID, id string) (Job, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM jobs WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return Job{}, err
	}
	if len(found) == 0 {
		return Job{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) byName(ctx context.Context, projectID, name string) (Job, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM jobs WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return Job{}, err
	}
	if len(found) == 0 {
		return Job{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) load(ctx context.Context, query string, args ...any) ([]Job, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]Job, 0, 8)
	for rows.Next() {
		j, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	for i := range jobs {
		runs, err := r.runsOf(ctx, jobs[i].ID)
		if err != nil {
			return nil, err
		}
		jobs[i].Runs = runs
	}
	return jobs, nil
}

func (r *repository) runsOf(ctx context.Context, jobID string) ([]Run, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, job_id, instance_id, attempt, state, message, exit_code,
			started_at, ended_at
			FROM job_runs WHERE job_id = ? ORDER BY started_at, id`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	runs := make([]Run, 0, 8)
	for rows.Next() {
		var (
			run     Run
			started string
			ended   string
		)
		if err := rows.Scan(&run.ID, &run.JobID, &run.InstanceID, &run.Attempt,
			&run.State, &run.Message, &run.ExitCode, &started, &ended); err != nil {
			return nil, fmt.Errorf("scan a run: %w", err)
		}

		at, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, fmt.Errorf("parse started_at: %w", err)
		}
		run.StartedAt = at
		if ended != "" {
			done, err := time.Parse(time.RFC3339Nano, ended)
			if err != nil {
				return nil, fmt.Errorf("parse ended_at: %w", err)
			}
			run.EndedAt = done
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (r *repository) insertRun(ctx context.Context, run Run) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO job_runs (id, job_id, instance_id, attempt, state, message, exit_code,
			started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.JobID, run.InstanceID, run.Attempt, run.State, run.Message, run.ExitCode,
		run.StartedAt.Format(time.RFC3339Nano), stamp(run.EndedAt))
	if err != nil {
		return fmt.Errorf("insert a run: %w", err)
	}
	return nil
}

func (r *repository) setRunState(ctx context.Context, runID, state string,
	exitCode *int, message string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE job_runs SET state = ?, exit_code = ?, message = ? WHERE id = ?`,
		state, exitCode, message, runID)
	if err != nil {
		return fmt.Errorf("set a run state: %w", err)
	}
	return nil
}

func (r *repository) endRun(ctx context.Context, runID, state string, exitCode *int,
	message string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE job_runs SET state = ?, exit_code = ?, message = ?, instance_id = '',
			ended_at = ? WHERE id = ?`,
		state, exitCode, message, at.Format(time.RFC3339Nano), runID)
	if err != nil {
		return fmt.Errorf("end a run: %w", err)
	}
	return nil
}

func (r *repository) deleteRun(ctx context.Context, runID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM job_runs WHERE id = ?`, runID)
	if err != nil {
		return fmt.Errorf("delete a run: %w", err)
	}
	return nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM job_runs WHERE job_id = ?`, id); err != nil {
		return fmt.Errorf("delete the runs: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	if err := oneRow(result); err != nil {
		return err
	}
	return tx.Commit()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Job, error) {
	var (
		j                Job
		template         string
		everySeconds     int64
		next, last       string
		created, updated string
	)
	if err := row.Scan(&j.ID, &j.ProjectID, &j.Name, &template, &j.EnvSealed, &j.FilesSealed,
		&j.SealKeyID, &everySeconds, &j.Retries, &j.Keep, &j.Paused, &next, &last,
		&created, &updated); err != nil {
		return Job{}, fmt.Errorf("scan job: %w", err)
	}

	if err := json.Unmarshal([]byte(template), &j.Template); err != nil {
		return Job{}, fmt.Errorf("decode the template: %w", err)
	}
	j.Every = time.Duration(everySeconds) * time.Second

	for _, one := range []struct {
		raw  string
		into *time.Time
	}{{next, &j.NextAt}, {last, &j.LastAt}} {
		if one.raw == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, one.raw)
		if err != nil {
			return Job{}, fmt.Errorf("parse a timestamp: %w", err)
		}
		*one.into = at
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Job{}, fmt.Errorf("parse created_at: %w", err)
	}
	j.CreatedAt = at

	touched, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Job{}, fmt.Errorf("parse updated_at: %w", err)
	}
	j.UpdatedAt = touched
	return j, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
