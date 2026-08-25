package node

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var errNotFound = errors.New("node not found")

const columns = `id, name, zone, address, arch, os, cpus, memory_mib, agent_version, registered_at, last_seen_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) findByName(ctx context.Context, name string) (Node, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM nodes WHERE name = ?`, name)

	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, errNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("find node by name: %w", err)
	}
	return n, nil
}

func (r *repository) insert(ctx context.Context, n Node) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO nodes (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Name, n.Zone, n.Address, n.Arch, n.OS, n.CPUs, n.MemoryMiB, n.AgentVersion,
		n.RegisteredAt.Format(time.RFC3339Nano), n.LastSeenAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert node: %w", err)
	}
	return nil
}

func (r *repository) updateOnRegister(ctx context.Context, n Node) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE nodes SET zone = ?, address = ?, arch = ?, os = ?, cpus = ?, memory_mib = ?,
		 agent_version = ?, last_seen_at = ? WHERE id = ?`,
		n.Zone, n.Address, n.Arch, n.OS, n.CPUs, n.MemoryMiB, n.AgentVersion,
		n.LastSeenAt.Format(time.RFC3339Nano), n.ID,
	)
	if err != nil {
		return fmt.Errorf("update node on register: %w", err)
	}
	return expectOneRow(res)
}

func (r *repository) touch(ctx context.Context, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE nodes SET last_seen_at = ? WHERE id = ?`,
		now.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("touch node: %w", err)
	}
	return expectOneRow(res)
}

func (r *repository) get(ctx context.Context, id string) (Node, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM nodes WHERE id = ?`, id)

	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, errNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("get node: %w", err)
	}
	return n, nil
}

func (r *repository) list(ctx context.Context) ([]Node, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM nodes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]Node, 0)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}
	return nodes, nil
}

func expectOneRow(res sql.Result) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNode(row scanner) (Node, error) {
	var (
		n             Node
		registeredRaw string
		lastSeenRaw   string
	)

	if err := row.Scan(
		&n.ID, &n.Name, &n.Zone, &n.Address, &n.Arch, &n.OS, &n.CPUs, &n.MemoryMiB, &n.AgentVersion,
		&registeredRaw, &lastSeenRaw,
	); err != nil {
		return Node{}, err
	}

	var err error
	if n.RegisteredAt, err = time.Parse(time.RFC3339Nano, registeredRaw); err != nil {
		return Node{}, fmt.Errorf("parse registered_at: %w", err)
	}
	if n.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenRaw); err != nil {
		return Node{}, fmt.Errorf("parse last_seen_at: %w", err)
	}
	return n, nil
}
