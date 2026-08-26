package image

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
	errNotFound  = errors.New("image not found")
	errNameTaken = errors.New("image name already exists")
)

const columns = `id, name, kind, arch, source, checksum, size_bytes, created_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, in Image) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO images (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, in.Kind, in.Arch, in.Source, in.Checksum, in.SizeBytes,
		in.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert image: %w", err)
	}
	return nil
}

func (r *repository) byID(ctx context.Context, id string) (Image, error) {
	return scanRow(r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM images WHERE id = ?`, id))
}

func (r *repository) byName(ctx context.Context, name string) (Image, error) {
	return scanRow(r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM images WHERE name = ?`, name))
}

func (r *repository) list(ctx context.Context) ([]Image, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM images ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	images := make([]Image, 0, 8)
	for rows.Next() {
		in, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		images = append(images, in)
	}
	return images, rows.Err()
}

func (r *repository) delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM images WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete image: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete image: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(row *sql.Row) (Image, error) {
	in, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, errNotFound
	}
	return in, err
}

func scan(row scanner) (Image, error) {
	var in Image
	var created string

	if err := row.Scan(&in.ID, &in.Name, &in.Kind, &in.Arch, &in.Source,
		&in.Checksum, &in.SizeBytes, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Image{}, err
		}
		return Image{}, fmt.Errorf("scan image: %w", err)
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Image{}, fmt.Errorf("parse created_at: %w", err)
	}
	in.CreatedAt = at
	return in, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
