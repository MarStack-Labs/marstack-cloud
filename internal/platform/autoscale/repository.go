package autoscale

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var errNotFound = errors.New("autoscale policy not found")

const columns = `id, project_id, service_id, min_replicas, max_replicas, target_cpu,
	target_memory, last_at, last_reason, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) upsert(ctx context.Context, p Policy) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO autoscalers (`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (service_id) DO UPDATE SET
			min_replicas = excluded.min_replicas,
			max_replicas = excluded.max_replicas,
			target_cpu = excluded.target_cpu,
			target_memory = excluded.target_memory,
			updated_at = excluded.updated_at`,
		p.ID, p.ProjectID, p.ServiceID, p.Min, p.Max, p.TargetCPU, p.TargetMemory,
		"", p.LastReason,
		p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save the policy: %w", err)
	}
	return nil
}

func (r *repository) setReason(ctx context.Context, id, reason string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE autoscalers SET last_reason = ? WHERE id = ?`, reason, id)
	if err != nil {
		return fmt.Errorf("record a reason: %w", err)
	}
	return nil
}

func (r *repository) setDecision(ctx context.Context, id, reason string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE autoscalers SET last_reason = ?, last_at = ?, updated_at = ? WHERE id = ?`,
		reason, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("record a decision: %w", err)
	}
	return nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM autoscalers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete the policy: %w", err)
	}
	return nil
}

func (r *repository) byService(ctx context.Context, serviceID string) (Policy, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM autoscalers WHERE service_id = ?`, serviceID)
	if err != nil {
		return Policy{}, err
	}
	if len(found) == 0 {
		return Policy{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Policy, error) {
	return r.load(ctx,
		`SELECT `+columns+` FROM autoscalers WHERE project_id = ? ORDER BY service_id`,
		projectID)
}

func (r *repository) all(ctx context.Context) ([]Policy, error) {
	return r.load(ctx, `SELECT `+columns+` FROM autoscalers ORDER BY id`)
}

func (r *repository) load(ctx context.Context, query string, args ...any) ([]Policy, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	defer rows.Close()

	policies := make([]Policy, 0, 4)
	for rows.Next() {
		var (
			p                Policy
			last             string
			created, updated string
		)
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.ServiceID, &p.Min, &p.Max,
			&p.TargetCPU, &p.TargetMemory, &last, &p.LastReason,
			&created, &updated); err != nil {
			return nil, fmt.Errorf("scan a policy: %w", err)
		}

		if last != "" {
			at, err := time.Parse(time.RFC3339Nano, last)
			if err != nil {
				return nil, fmt.Errorf("parse last_at: %w", err)
			}
			p.LastAt = at
		}

		at, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		p.CreatedAt = at

		touched, err := time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		p.UpdatedAt = touched

		policies = append(policies, p)
	}
	return policies, rows.Err()
}
