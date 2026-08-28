package webhook

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
	errNotFound = errors.New("webhook not found")
	errNameUsed = errors.New("webhook name already used")
)

const columns = `id, project_id, name, url, secret, kinds, active, created_at`

const deliveryColumns = `id, subscription_id, event_id, kind, subject, state, attempts,
	last_error, next_attempt_at, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, s Subscription) error {
	kinds, err := json.Marshal(s.Kinds)
	if err != nil {
		return fmt.Errorf("encode the kinds: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO webhooks (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, s.Name, s.URL, s.Secret, string(kinds), s.Active,
		s.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameUsed
		}
		return fmt.Errorf("insert webhook: %w", err)
	}
	return nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Subscription, error) {
	return r.load(ctx,
		`SELECT `+columns+` FROM webhooks WHERE project_id = ? ORDER BY name`, projectID)
}

func (r *repository) active(ctx context.Context) ([]Subscription, error) {
	return r.load(ctx, `SELECT `+columns+` FROM webhooks WHERE active = 1 ORDER BY id`)
}

func (r *repository) byID(ctx context.Context, projectID, id string) (Subscription, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM webhooks WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return Subscription{}, err
	}
	if len(found) == 0 {
		return Subscription{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) byName(ctx context.Context, projectID, name string) (Subscription, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM webhooks WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return Subscription{}, err
	}
	if len(found) == 0 {
		return Subscription{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) load(ctx context.Context, query string, args ...any) ([]Subscription, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()

	subscriptions := make([]Subscription, 0, 8)
	for rows.Next() {
		var (
			s       Subscription
			kinds   string
			created string
		)
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.URL, &s.Secret, &kinds,
			&s.Active, &created); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		if err := json.Unmarshal([]byte(kinds), &s.Kinds); err != nil {
			return nil, fmt.Errorf("decode the kinds: %w", err)
		}

		at, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		s.CreatedAt = at
		subscriptions = append(subscriptions, s)
	}
	return subscriptions, rows.Err()
}

func (r *repository) setActive(ctx context.Context, id string, active bool) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE webhooks SET active = ? WHERE id = ?`, active, id)
	if err != nil {
		return fmt.Errorf("set the webhook state: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set the webhook state: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE subscription_id = ?`, id); err != nil {
		return fmt.Errorf("delete the deliveries: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return tx.Commit()
}

func (r *repository) cursor(ctx context.Context) (int64, error) {
	var at sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT last_event_id FROM webhook_cursor WHERE id = 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the webhook cursor: %w", err)
	}
	return at.Int64, nil
}

func (r *repository) setCursor(ctx context.Context, at int64) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO webhook_cursor (id, last_event_id) VALUES (1, ?)
			ON CONFLICT (id) DO UPDATE SET last_event_id = excluded.last_event_id`, at)
	if err != nil {
		return fmt.Errorf("move the webhook cursor: %w", err)
	}
	return nil
}

func (r *repository) enqueue(ctx context.Context, deliveries []Delivery, at int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	for _, d := range deliveries {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO webhook_deliveries (`+deliveryColumns+`)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.SubscriptionID, d.EventID, d.Kind, d.Subject, d.State, d.Attempts,
			d.LastError, d.NextAttemptAt.Format(time.RFC3339Nano),
			d.CreatedAt.Format(time.RFC3339Nano), d.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("enqueue a delivery: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO webhook_cursor (id, last_event_id) VALUES (1, ?)
			ON CONFLICT (id) DO UPDATE SET last_event_id = excluded.last_event_id`,
		at); err != nil {
		return fmt.Errorf("move the webhook cursor: %w", err)
	}
	return tx.Commit()
}

func (r *repository) due(ctx context.Context, now time.Time, limit int) ([]Delivery, error) {
	return r.loadDeliveries(ctx,
		`SELECT `+deliveryColumns+` FROM webhook_deliveries
			WHERE state = ? AND next_attempt_at <= ?
			ORDER BY next_attempt_at LIMIT ?`,
		StatePending, now.Format(time.RFC3339Nano), limit)
}

func (r *repository) deliveriesOf(ctx context.Context, subscriptionID string, limit int) ([]Delivery, error) {
	return r.loadDeliveries(ctx,
		`SELECT `+deliveryColumns+` FROM webhook_deliveries
			WHERE subscription_id = ? ORDER BY id DESC LIMIT ?`, subscriptionID, limit)
}

func (r *repository) loadDeliveries(ctx context.Context, query string, args ...any) ([]Delivery, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()

	deliveries := make([]Delivery, 0, 16)
	for rows.Next() {
		var (
			d                      Delivery
			next, created, updated string
		)
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.EventID, &d.Kind, &d.Subject,
			&d.State, &d.Attempts, &d.LastError, &next, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan a delivery: %w", err)
		}

		for target, raw := range map[*time.Time]string{
			&d.NextAttemptAt: next, &d.CreatedAt: created, &d.UpdatedAt: updated,
		} {
			at, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, fmt.Errorf("parse a delivery timestamp: %w", err)
			}
			*target = at
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func (r *repository) markDelivery(ctx context.Context, d Delivery) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE webhook_deliveries SET state = ?, attempts = ?, last_error = ?,
			next_attempt_at = ?, updated_at = ? WHERE id = ?`,
		d.State, d.Attempts, d.LastError, d.NextAttemptAt.Format(time.RFC3339Nano),
		d.UpdatedAt.Format(time.RFC3339Nano), d.ID)
	if err != nil {
		return fmt.Errorf("mark a delivery: %w", err)
	}
	return nil
}

func (r *repository) pruneDeliveries(ctx context.Context, retain int) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE state != ?
			AND rowid <= (SELECT MAX(rowid) - ? FROM webhook_deliveries)`,
		StatePending, retain)
	if err != nil {
		return fmt.Errorf("prune deliveries: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
