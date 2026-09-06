package store

import (
	"context"
	"fmt"
	"time"
)

type Migration struct {
	Module string
	Index  int
	SQL    string
}

const migrationTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	module     TEXT    NOT NULL,
	idx        INTEGER NOT NULL,
	applied_at TEXT    NOT NULL,
	PRIMARY KEY (module, idx)
)`

type migrationKey struct {
	module string
	index  int
}

func (s *Store) Migrate(ctx context.Context, migrations []Migration) error {
	if _, err := s.db.ExecContext(ctx, migrationTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	pending := make([]Migration, 0, len(migrations))
	for _, m := range migrations {
		if !applied[migrationKey{module: m.Module, index: m.Index}] {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()

	at := time.Now().UTC().Format(time.RFC3339)
	for _, m := range pending {
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("apply migration %s/%d: %w", m.Module, m.Index, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (module, idx, applied_at) VALUES (?, ?, ?)`,
			m.Module, m.Index, at,
		); err != nil {
			return fmt.Errorf("record migration %s/%d: %w", m.Module, m.Index, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func (s *Store) appliedMigrations(ctx context.Context) (map[migrationKey]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT module, idx FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[migrationKey]bool{}
	for rows.Next() {
		var key migrationKey
		if err := rows.Scan(&key.module, &key.index); err != nil {
			return nil, fmt.Errorf("read schema_migrations: %w", err)
		}
		applied[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}
