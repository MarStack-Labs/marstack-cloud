package volume

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

func New(st *store.Store, instances Instances, log *slog.Logger) *Module {
	svc := newService(newRepository(st), instances, nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "volume"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "volume",
			Index:  1,
			SQL: `CREATE TABLE volumes (
				id          TEXT PRIMARY KEY,
				name        TEXT NOT NULL,
				size_gib    INTEGER NOT NULL,
				node_id     TEXT NOT NULL DEFAULT '',
				instance_id TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL,
				updated_at  TEXT NOT NULL
			)`,
		},
		{
			Module: "volume",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX volumes_name_unique ON volumes (name)`,
		},
		{
			Module: "volume",
			Index:  3,
			SQL:    `CREATE INDEX volumes_node_id ON volumes (node_id)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/volumes", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/volumes", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/volumes/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/volumes/{id}", httpx.Wrap(m.log, m.handler.delete))
	mux.Handle("POST /v1/volumes/{id}/attach", httpx.Wrap(m.log, m.handler.attach))
	mux.Handle("POST /v1/volumes/{id}/detach", httpx.Wrap(m.log, m.handler.detach))

	mux.Handle("GET /v1/nodes/{nodeID}/volumes", httpx.Wrap(m.log, m.handler.listForNode))
}

func (m *Module) ReleaseInstance(ctx context.Context, instanceID string) error {
	return m.svc.releaseInstance(ctx, instanceID)
}
