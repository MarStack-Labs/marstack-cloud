package forward

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type Module struct {
	log     *slog.Logger
	svc     *service
	handler *handler
}

func New(st *store.Store, addresses Addresses, log *slog.Logger) *Module {
	svc := newService(newRepository(st), addresses, nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "forward"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "forward",
			Index:  1,
			SQL: `CREATE TABLE forwards (
				id          TEXT PRIMARY KEY,
				instance_id TEXT NOT NULL,
				protocol    TEXT NOT NULL,
				node_port   INTEGER NOT NULL,
				target_port INTEGER NOT NULL,
				node_id     TEXT NOT NULL,
				address     TEXT NOT NULL,
				created_at  TEXT NOT NULL
			)`,
		},
		{
			Module: "forward",
			Index:  2,
			SQL: `CREATE UNIQUE INDEX forwards_node_port_unique
				ON forwards (node_id, protocol, node_port)`,
		},
		{
			Module: "forward",
			Index:  3,
			SQL:    `CREATE INDEX forwards_instance_id ON forwards (instance_id)`,
		},
		{
			Module: "forward",
			Index:  4,
			SQL:    `ALTER TABLE forwards ADD COLUMN project_id TEXT NOT NULL DEFAULT 'prj-default'`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/forwards", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/forwards", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("DELETE /v1/forwards/{id}", httpx.Wrap(m.log, m.handler.delete))

	mux.Handle("GET /v1/nodes/{nodeID}/forwards", httpx.Wrap(m.log, m.handler.listForNode))
}

func (m *Module) ReleaseInstance(ctx context.Context, instanceID string) error {
	return m.svc.releaseInstance(ctx, instanceID)
}
