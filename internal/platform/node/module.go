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
		if n.StatusAt(now) == StatusReady {
			ready = append(ready, n)
		}
	}
	return ready, nil
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
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/nodes/register", httpx.Wrap(m.log, m.handler.register))
	mux.Handle("POST /v1/nodes/{id}/heartbeat", httpx.Wrap(m.log, m.handler.heartbeat))
	mux.Handle("GET /v1/nodes", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/nodes/{id}", httpx.Wrap(m.log, m.handler.get))
}
