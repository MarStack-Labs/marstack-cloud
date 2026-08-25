package network

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
	errNotFound  = errors.New("network not found")
	errNameTaken = errors.New("network name already exists")
	errCIDRTaken = errors.New("address already allocated")
)

const networkColumns = `id, name, cidr, gateway, bridge, created_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insertNetwork(ctx context.Context, n Network) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO networks (`+networkColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		n.ID, n.Name, n.CIDR, n.Gateway, n.Bridge, n.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert network: %w", err)
	}
	return nil
}

func (r *repository) networkByName(ctx context.Context, name string) (Network, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+networkColumns+` FROM networks WHERE name = ?`, name)
	return scanNetworkRow(row)
}

func (r *repository) network(ctx context.Context, id string) (Network, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+networkColumns+` FROM networks WHERE id = ?`, id)
	return scanNetworkRow(row)
}

func scanNetworkRow(row *sql.Row) (Network, error) {
	n, err := scanNetwork(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Network{}, errNotFound
	}
	if err != nil {
		return Network{}, fmt.Errorf("read network: %w", err)
	}
	return n, nil
}

func (r *repository) listNetworks(ctx context.Context) ([]Network, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+networkColumns+` FROM networks ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	defer rows.Close()

	networks := make([]Network, 0)
	for rows.Next() {
		n, err := scanNetwork(rows)
		if err != nil {
			return nil, fmt.Errorf("scan network: %w", err)
		}
		networks = append(networks, n)
	}
	return networks, rows.Err()
}

func (r *repository) slice(ctx context.Context, networkID, nodeID string) (Slice, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT network_id, node_id, cidr, created_at FROM node_slices WHERE network_id = ? AND node_id = ?`,
		networkID, nodeID,
	)

	var (
		s          Slice
		createdRaw string
	)
	if err := row.Scan(&s.NetworkID, &s.NodeID, &s.CIDR, &createdRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Slice{}, errNotFound
		}
		return Slice{}, fmt.Errorf("read slice: %w", err)
	}

	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return Slice{}, fmt.Errorf("parse slice created_at: %w", err)
	}
	s.CreatedAt = created
	return s, nil
}

func (r *repository) takenSlices(ctx context.Context, networkID string) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT cidr FROM node_slices WHERE network_id = ?`, networkID)
	if err != nil {
		return nil, fmt.Errorf("list slices: %w", err)
	}
	defer rows.Close()

	taken := map[string]bool{}
	for rows.Next() {
		var cidr string
		if err := rows.Scan(&cidr); err != nil {
			return nil, fmt.Errorf("scan slice: %w", err)
		}
		taken[cidr] = true
	}
	return taken, rows.Err()
}

func (r *repository) slicesExcept(ctx context.Context, networkID, nodeID string) ([]Slice, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT network_id, node_id, cidr, created_at FROM node_slices
		 WHERE network_id = ? AND node_id != ? ORDER BY cidr`,
		networkID, nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list peer slices: %w", err)
	}
	defer rows.Close()

	slices := make([]Slice, 0)
	for rows.Next() {
		var (
			s          Slice
			createdRaw string
		)
		if err := rows.Scan(&s.NetworkID, &s.NodeID, &s.CIDR, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan peer slice: %w", err)
		}
		created, err := time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, fmt.Errorf("parse peer slice created_at: %w", err)
		}
		s.CreatedAt = created
		slices = append(slices, s)
	}
	return slices, rows.Err()
}

func (r *repository) insertSlice(ctx context.Context, s Slice) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO node_slices (network_id, node_id, cidr, created_at) VALUES (?, ?, ?, ?)`,
		s.NetworkID, s.NodeID, s.CIDR, s.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errCIDRTaken
		}
		return fmt.Errorf("insert slice: %w", err)
	}
	return nil
}

func (r *repository) nic(ctx context.Context, instanceID string) (NIC, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT instance_id, network_id, node_id, ip, mac, created_at FROM nics WHERE instance_id = ?`,
		instanceID,
	)
	return scanNICRow(row)
}

func scanNICRow(row *sql.Row) (NIC, error) {
	var (
		n          NIC
		createdRaw string
	)
	if err := row.Scan(&n.InstanceID, &n.NetworkID, &n.NodeID, &n.IP, &n.MAC, &createdRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NIC{}, errNotFound
		}
		return NIC{}, fmt.Errorf("read nic: %w", err)
	}

	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return NIC{}, fmt.Errorf("parse nic created_at: %w", err)
	}
	n.CreatedAt = created
	return n, nil
}

func (r *repository) takenAddresses(ctx context.Context, networkID string) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT ip FROM nics WHERE network_id = ?`, networkID)
	if err != nil {
		return nil, fmt.Errorf("list addresses: %w", err)
	}
	defer rows.Close()

	taken := map[string]bool{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan address: %w", err)
		}
		taken[ip] = true
	}
	return taken, rows.Err()
}

func (r *repository) insertNIC(ctx context.Context, n NIC) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO nics (instance_id, network_id, node_id, ip, mac, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		n.InstanceID, n.NetworkID, n.NodeID, n.IP, n.MAC, n.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errCIDRTaken
		}
		return fmt.Errorf("insert nic: %w", err)
	}
	return nil
}

func (r *repository) nicsOnNode(ctx context.Context, nodeID string) ([]NIC, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT instance_id, network_id, node_id, ip, mac, created_at FROM nics WHERE node_id = ? ORDER BY ip`,
		nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list nics on node: %w", err)
	}
	defer rows.Close()

	nics := make([]NIC, 0)
	for rows.Next() {
		var (
			n          NIC
			createdRaw string
		)
		if err := rows.Scan(&n.InstanceID, &n.NetworkID, &n.NodeID, &n.IP, &n.MAC, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan nic: %w", err)
		}
		created, err := time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, fmt.Errorf("parse nic created_at: %w", err)
		}
		n.CreatedAt = created
		nics = append(nics, n)
	}
	return nics, rows.Err()
}

func (r *repository) allAddresses(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT instance_id, ip FROM nics`)
	if err != nil {
		return nil, fmt.Errorf("list all addresses: %w", err)
	}
	defer rows.Close()

	addresses := map[string]string{}
	for rows.Next() {
		var instanceID, ip string
		if err := rows.Scan(&instanceID, &ip); err != nil {
			return nil, fmt.Errorf("scan address: %w", err)
		}
		addresses[instanceID] = ip
	}
	return addresses, rows.Err()
}

func (r *repository) deleteNIC(ctx context.Context, instanceID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM nics WHERE instance_id = ?`, instanceID); err != nil {
		return fmt.Errorf("delete nic: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNetwork(row scanner) (Network, error) {
	var (
		n          Network
		createdRaw string
	)
	if err := row.Scan(&n.ID, &n.Name, &n.CIDR, &n.Gateway, &n.Bridge, &createdRaw); err != nil {
		return Network{}, err
	}

	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return Network{}, fmt.Errorf("parse network created_at: %w", err)
	}
	n.CreatedAt = created
	return n, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
