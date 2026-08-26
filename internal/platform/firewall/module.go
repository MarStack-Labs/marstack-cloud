package firewall

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

func New(st *store.Store, log *slog.Logger) *Module {
	svc := newService(newRepository(st), nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "firewall"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "firewall",
			Index:  1,
			SQL: `CREATE TABLE firewalls (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				rules      TEXT NOT NULL DEFAULT '[]',
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
		},
		{
			Module: "firewall",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX firewalls_name_unique ON firewalls (name)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/firewalls", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/firewalls", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/firewalls/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("PUT /v1/firewalls/{id}/rules", httpx.Wrap(m.log, m.handler.setRules))
	mux.Handle("DELETE /v1/firewalls/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) Exists(ctx context.Context, id string) (bool, error) {
	return m.svc.exists(ctx, id)
}
