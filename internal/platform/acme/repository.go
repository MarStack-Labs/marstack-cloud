package acme

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var errNotFound = errors.New("order not found")

const columns = `id, project_id, balancer_id, names, state, message, order_url, key_pem, ` +
	`challenges, expires_at, issued_at, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) save(ctx context.Context, o Order) error {
	names, err := json.Marshal(o.Names)
	if err != nil {
		return fmt.Errorf("encode the names: %w", err)
	}
	challenges, err := json.Marshal(o.Challenges)
	if err != nil {
		return fmt.Errorf("encode the challenges: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO acme_orders (`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				names = excluded.names, state = excluded.state, message = excluded.message,
				order_url = excluded.order_url, key_pem = excluded.key_pem,
				challenges = excluded.challenges, expires_at = excluded.expires_at,
				issued_at = excluded.issued_at, updated_at = excluded.updated_at`,
		o.ID, o.ProjectID, o.BalancerID, string(names), o.State, o.Message, o.OrderURL,
		o.KeyPEM, string(challenges), o.ExpiresAt,
		o.IssuedAt.Format(time.RFC3339Nano),
		o.CreatedAt.Format(time.RFC3339Nano), o.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save the order: %w", err)
	}
	return nil
}

func (r *repository) all(ctx context.Context) ([]Order, error) {
	return r.query(ctx, `SELECT `+columns+` FROM acme_orders ORDER BY created_at`)
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Order, error) {
	return r.query(ctx,
		`SELECT `+columns+` FROM acme_orders WHERE project_id = ? ORDER BY created_at`,
		projectID)
}

func (r *repository) forBalancer(ctx context.Context, balancerID string) (Order, error) {
	held, err := r.query(ctx,
		`SELECT `+columns+` FROM acme_orders WHERE balancer_id = ?`, balancerID)
	if err != nil {
		return Order{}, err
	}
	if len(held) == 0 {
		return Order{}, errNotFound
	}
	return held[0], nil
}

func (r *repository) delete(ctx context.Context, balancerID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM acme_orders WHERE balancer_id = ? OR id = ?`, balancerID, balancerID)
	if err != nil {
		return fmt.Errorf("delete the order: %w", err)
	}
	return nil
}

func (r *repository) query(ctx context.Context, sql string, args ...any) ([]Order, error) {
	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	held := make([]Order, 0, 4)
	for rows.Next() {
		var o Order
		var names, challenges, issued, created, updated string

		if err := rows.Scan(&o.ID, &o.ProjectID, &o.BalancerID, &names, &o.State, &o.Message,
			&o.OrderURL, &o.KeyPEM, &challenges, &o.ExpiresAt,
			&issued, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan an order: %w", err)
		}

		_ = json.Unmarshal([]byte(names), &o.Names)
		_ = json.Unmarshal([]byte(challenges), &o.Challenges)
		o.IssuedAt, _ = time.Parse(time.RFC3339Nano, issued)
		o.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		o.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		held = append(held, o)
	}
	return held, rows.Err()
}

func (r *repository) account(ctx context.Context) (string, string, error) {
	var key, url string
	err := r.db.QueryRowContext(ctx,
		`SELECT key_pem, url FROM acme_account WHERE id = 1`).Scan(&key, &url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("read the account: %w", err)
	}
	return key, url, nil
}

func (r *repository) saveAccount(ctx context.Context, key, url string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO acme_account (id, key_pem, url) VALUES (1, ?, ?)
			ON CONFLICT(id) DO UPDATE SET key_pem = excluded.key_pem, url = excluded.url`,
		key, url)
	if err != nil {
		return fmt.Errorf("save the account: %w", err)
	}
	return nil
}
