package firewall

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
	errNotFound  = errors.New("firewall not found")
	errNameTaken = errors.New("firewall name already exists")
)

const columns = `id, name, rules, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, f Firewall) error {
	rules, err := json.Marshal(f.Rules)
	if err != nil {
		return fmt.Errorf("encode rules: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO firewalls (`+columns+`) VALUES (?, ?, ?, ?, ?)`,
		f.ID, f.Name, string(rules),
		f.CreatedAt.Format(time.RFC3339Nano), f.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert firewall: %w", err)
	}
	return nil
}

func (r *repository) replaceRules(ctx context.Context, id string, rules []Rule, at time.Time) error {
	encoded, err := json.Marshal(rules)
	if err != nil {
		return fmt.Errorf("encode rules: %w", err)
	}

	result, err := r.db.ExecContext(ctx,
		`UPDATE firewalls SET rules = ?, updated_at = ? WHERE id = ?`,
		string(encoded), at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("replace rules: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("replace rules: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) byID(ctx context.Context, id string) (Firewall, error) {
	return scanRow(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM firewalls WHERE id = ?`, id))
}

func (r *repository) byName(ctx context.Context, name string) (Firewall, error) {
	return scanRow(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM firewalls WHERE name = ?`, name))
}

func (r *repository) list(ctx context.Context) ([]Firewall, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM firewalls ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list firewalls: %w", err)
	}
	defer rows.Close()

	firewalls := make([]Firewall, 0, 8)
	for rows.Next() {
		f, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		firewalls = append(firewalls, f)
	}
	return firewalls, rows.Err()
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM firewalls WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete firewall: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete firewall: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(row *sql.Row) (Firewall, error) {
	f, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Firewall{}, errNotFound
	}
	return f, err
}

func scan(row scanner) (Firewall, error) {
	var f Firewall
	var rules, created, updated string

	if err := row.Scan(&f.ID, &f.Name, &rules, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Firewall{}, err
		}
		return Firewall{}, fmt.Errorf("scan firewall: %w", err)
	}

	if rules != "" {
		if err := json.Unmarshal([]byte(rules), &f.Rules); err != nil {
			return Firewall{}, fmt.Errorf("decode rules: %w", err)
		}
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Firewall{}, fmt.Errorf("parse created_at: %w", err)
	}
	f.CreatedAt = at

	at, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Firewall{}, fmt.Errorf("parse updated_at: %w", err)
	}
	f.UpdatedAt = at

	return f, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
