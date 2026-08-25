package node

import (
	"log/slog"
	"net/http"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type Module struct {
	log     *slog.Logger
	handler *handler
}

func New(st *store.Store, log *slog.Logger) *Module {
	return &Module{
		log:     log,
		handler: &handler{svc: newService(newRepository(st), nil)},
	}
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
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/nodes/register", httpx.Wrap(m.log, m.handler.register))
	mux.Handle("POST /v1/nodes/{id}/heartbeat", httpx.Wrap(m.log, m.handler.heartbeat))
	mux.Handle("GET /v1/nodes", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/nodes/{id}", httpx.Wrap(m.log, m.handler.get))
}
