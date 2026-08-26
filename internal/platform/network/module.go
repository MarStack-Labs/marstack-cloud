package network

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type NodeNetwork struct {
	Network Network
	Slice   Slice
	NICs    []NIC
	Peers   []Slice
}

type Module struct {
	log     *slog.Logger
	svc     *service
	handler *handler
}

func New(st *store.Store, log *slog.Logger) *Module {
	svc := newService(newRepository(st), nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "network"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "network",
			Index:  1,
			SQL: `CREATE TABLE networks (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				cidr       TEXT NOT NULL,
				gateway    TEXT NOT NULL,
				bridge     TEXT NOT NULL,
				created_at TEXT NOT NULL
			)`,
		},
		{
			Module: "network",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX networks_name_unique ON networks (name)`,
		},
		{
			Module: "network",
			Index:  3,
			SQL: `CREATE TABLE node_slices (
				network_id TEXT NOT NULL,
				node_id    TEXT NOT NULL,
				cidr       TEXT NOT NULL,
				created_at TEXT NOT NULL,
				PRIMARY KEY (network_id, node_id)
			)`,
		},
		{
			Module: "network",
			Index:  4,
			SQL:    `CREATE UNIQUE INDEX node_slices_cidr_unique ON node_slices (network_id, cidr)`,
		},
		{
			Module: "network",
			Index:  5,
			SQL: `CREATE TABLE nics (
				instance_id TEXT PRIMARY KEY,
				network_id  TEXT NOT NULL,
				node_id     TEXT NOT NULL,
				ip          TEXT NOT NULL,
				mac         TEXT NOT NULL,
				created_at  TEXT NOT NULL
			)`,
		},
		{
			Module: "network",
			Index:  6,
			SQL:    `CREATE UNIQUE INDEX nics_address_unique ON nics (network_id, ip)`,
		},
		{
			Module: "network",
			Index:  7,
			SQL:    `CREATE INDEX nics_node_id ON nics (node_id)`,
		},
		{
			Module: "network",
			Index:  8,
			SQL:    `CREATE UNIQUE INDEX networks_cidr_unique ON networks (cidr)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/networks", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/networks", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/networks/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/networks/{id}", httpx.Wrap(m.log, m.handler.delete))
	mux.Handle("GET /v1/nodes/{nodeID}/network", httpx.Wrap(m.log, m.handler.nodeView))
}

func (m *Module) EnsureDefault(ctx context.Context) (Network, error) {
	n, err := m.svc.ensureDefault(ctx)
	if err != nil {
		return Network{}, err
	}
	m.log.Info("default network ready", "network", n.Name, "cidr", n.CIDR, "bridge", n.Bridge)
	return n, nil
}

func (m *Module) DefaultNetworkID(ctx context.Context) (string, error) {
	n, err := m.svc.ensureDefault(ctx)
	if err != nil {
		return "", err
	}
	return n.ID, nil
}

func (m *Module) Allocate(ctx context.Context, instanceID, networkID, nodeID string) error {
	nic, err := m.svc.allocate(ctx, instanceID, networkID, nodeID)
	if err != nil {
		return err
	}
	m.log.Info("address allocated", "instance", instanceID, "ip", nic.IP, "node", nodeID)
	return nil
}

func (m *Module) ReleaseAddress(ctx context.Context, instanceID string) error {
	return m.svc.release(ctx, instanceID)
}

func (m *Module) NICOf(ctx context.Context, instanceID string) (NIC, error) {
	return m.svc.nicOf(ctx, instanceID)
}

func (m *Module) AllAddresses(ctx context.Context) (map[string]string, error) {
	return m.svc.allAddresses(ctx)
}

func (m *Module) NetworkNames(ctx context.Context) (map[string]string, error) {
	networks, err := m.svc.list(ctx)
	if err != nil {
		return nil, err
	}

	names := make(map[string]string, len(networks))
	for _, n := range networks {
		names[n.ID] = n.Name
	}
	return names, nil
}

func (m *Module) SliceFor(ctx context.Context, networkID, nodeID string) (Slice, error) {
	return m.svc.ensureSlice(ctx, networkID, nodeID)
}
