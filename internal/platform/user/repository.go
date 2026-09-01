package user

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
	errNotFound  = errors.New("user not found")
	errEmailUsed = errors.New("email already used")
)

const columns = `id, email, name, role, project_id, disabled, created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, u User, stored string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO users (`+columns+`, password) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.Name, u.Role, u.ProjectID, u.Disabled,
		u.CreatedAt.Format(time.RFC3339Nano), u.UpdatedAt.Format(time.RFC3339Nano), stored)
	if err != nil {
		if isUniqueViolation(err) {
			return errEmailUsed
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (r *repository) setPassword(ctx context.Context, id, stored string, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE users SET password = ?, updated_at = ? WHERE id = ?`,
		stored, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set the password: %w", err)
	}
	return oneRow(result)
}

func (r *repository) setDisabled(ctx context.Context, id string, disabled bool,
	at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`,
		disabled, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set disabled: %w", err)
	}
	return oneRow(result)
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return oneRow(result)
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

func (r *repository) byID(ctx context.Context, id string) (User, error) {
	return r.one(ctx, `SELECT `+columns+` FROM users WHERE id = ?`, id)
}

func (r *repository) byEmail(ctx context.Context, email string) (User, error) {
	return r.one(ctx, `SELECT `+columns+` FROM users WHERE email = ?`, email)
}

func (r *repository) credentials(ctx context.Context, email string) (User, string, error) {
	var (
		u                User
		stored           string
		created, updated string
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT `+columns+`, password FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.ProjectID, &u.Disabled,
		&created, &updated, &stored)

	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", errNotFound
	}
	if err != nil {
		return User{}, "", fmt.Errorf("look up credentials: %w", err)
	}
	if u.CreatedAt, u.UpdatedAt, err = stamps(created, updated); err != nil {
		return User{}, "", err
	}
	return u, stored, nil
}

func (r *repository) one(ctx context.Context, query string, args ...any) (User, error) {
	found, err := r.load(ctx, query, args...)
	if err != nil {
		return User{}, err
	}
	if len(found) == 0 {
		return User{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) list(ctx context.Context) ([]User, error) {
	return r.load(ctx, `SELECT `+columns+` FROM users ORDER BY email`)
}

func (r *repository) load(ctx context.Context, query string, args ...any) ([]User, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	people := make([]User, 0, 8)
	for rows.Next() {
		var (
			u                User
			created, updated string
		)
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.ProjectID, &u.Disabled,
			&created, &updated); err != nil {
			return nil, fmt.Errorf("scan a user: %w", err)
		}
		if u.CreatedAt, u.UpdatedAt, err = stamps(created, updated); err != nil {
			return nil, err
		}
		people = append(people, u)
	}
	return people, rows.Err()
}

func stamps(created, updated string) (time.Time, time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse created_at: %w", err)
	}
	touched, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return at, touched, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
