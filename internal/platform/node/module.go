package node

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

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

func (m *Module) NodesUnreachableFor(ctx context.Context, grace time.Duration) ([]Node, error) {
	nodes, err := m.svc.list(ctx)
	if err != nil {
		return nil, err
	}

	now := m.svc.now()
	stranded := make([]Node, 0)
	for _, n := range nodes {
		if n.StatusAt(now) == StatusReady {
			continue
		}
		if now.Sub(n.LastSeenAt) >= grace {
			stranded = append(stranded, n)
		}
	}
	return stranded, nil
}

func (m *Module) ReadyNodes(ctx context.Context) ([]Node, error) {
	nodes, err := m.svc.list(ctx)
	if err != nil {
		return nil, err
	}

	now := m.svc.now()
	ready := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.StatusAt(now) != StatusReady {
			continue
		}
		if !n.Schedulable {
			continue
		}
		ready = append(ready, n)
	}
	return ready, nil
}

func (m *Module) DrainingNodes(ctx context.Context) ([]Node, error) {
	nodes, err := m.svc.list(ctx)
	if err != nil {
		return nil, err
	}

	draining := make([]Node, 0)
	for _, n := range nodes {
		if n.Draining {
			draining = append(draining, n)
		}
	}
	return draining, nil
}

func (m *Module) FinishDraining(ctx context.Context, nodeID string) error {
	return m.svc.setDraining(ctx, nodeID, false)
}

func (m *Module) Name() string {
	return "node"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "node",
			Index:  1,
			SQL: `CREATE TABLE nodes (
				id            TEXT    PRIMARY KEY,
				name          TEXT    NOT NULL,
				zone          TEXT    NOT NULL DEFAULT '',
				arch          TEXT    NOT NULL,
				os            TEXT    NOT NULL,
				cpus          INTEGER NOT NULL,
				memory_mib    INTEGER NOT NULL,
				agent_version TEXT    NOT NULL,
				registered_at TEXT    NOT NULL,
				last_seen_at  TEXT    NOT NULL
			)`,
		},
		{
			Module: "node",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX nodes_name_unique ON nodes (name)`,
		},
		{
			Module: "node",
			Index:  3,
			SQL:    `ALTER TABLE nodes ADD COLUMN address TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "node",
			Index:  4,
			SQL:    `ALTER TABLE nodes ADD COLUMN schedulable INTEGER NOT NULL DEFAULT 1`,
		},
		{
			Module: "node",
			Index:  5,
			SQL:    `ALTER TABLE nodes ADD COLUMN draining INTEGER NOT NULL DEFAULT 0`,
		},
		{
			Module: "node",
			Index:  6,
			SQL: `CREATE TABLE node_labels (
				node_id TEXT NOT NULL,
				key     TEXT NOT NULL,
				value   TEXT NOT NULL,
				PRIMARY KEY (node_id, key)
			)`,
		},
		{
			Module: "node",
			Index:  7,
			SQL:    `CREATE INDEX node_labels_lookup ON node_labels (key, value)`,
		},
		{
			Module: "node",
			Index:  8,
			SQL: `CREATE TABLE node_devices (
				node_id     TEXT NOT NULL,
				address     TEXT NOT NULL,
				kind        TEXT NOT NULL,
				vendor      TEXT NOT NULL DEFAULT '',
				product     TEXT NOT NULL DEFAULT '',
				driver      TEXT NOT NULL DEFAULT '',
				ready       INTEGER NOT NULL DEFAULT 0,
				instance_id TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (node_id, address)
			)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/nodes/register", httpx.Wrap(m.log, m.handler.register))
	mux.Handle("POST /v1/nodes/{id}/heartbeat", httpx.Wrap(m.log, m.handler.heartbeat))
	mux.Handle("GET /v1/nodes", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/nodes/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("POST /v1/nodes/{id}/cordon", httpx.Wrap(m.log, m.handler.cordon))
	mux.Handle("POST /v1/nodes/{id}/uncordon", httpx.Wrap(m.log, m.handler.uncordon))
	mux.Handle("POST /v1/nodes/{id}/drain", httpx.Wrap(m.log, m.handler.drain))
	mux.Handle("PUT /v1/nodes/{id}/labels", httpx.Wrap(m.log, m.handler.setLabels))
	mux.Handle("PUT /v1/nodes/{id}/devices", httpx.Wrap(m.log, m.handler.setDevices))
	mux.Handle("GET /v1/devices", httpx.Wrap(m.log, m.handler.listDevices))
}

func (m *Module) FreeDevice(
	ctx context.Context, nodeID, kind string,
) (string, bool, error) {
	return m.svc.FreeDevice(ctx, nodeID, kind)
}

func (m *Module) ClaimDevice(
	ctx context.Context, nodeID, kind, instanceID string,
) (string, error) {
	return m.svc.ClaimDevice(ctx, nodeID, kind, instanceID)
}

func (m *Module) ReleaseInstance(ctx context.Context, instanceID string) error {
	return m.svc.ReleaseInstance(ctx, instanceID)
}

func (m *Module) AddressOf(ctx context.Context, instanceID string) (string, error) {
	held, err := m.svc.DevicesOf(ctx, instanceID)
	if err != nil || len(held) == 0 {
		return "", err
	}
	return held[0].Address, nil
}

func (m *Module) DevicesOf(ctx context.Context, instanceID string) ([]Device, error) {
	return m.svc.DevicesOf(ctx, instanceID)
}
