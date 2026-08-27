package quota

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
	return "quota"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "quota",
			Index:  1,
			SQL: `CREATE TABLE quotas (
				project_id TEXT    PRIMARY KEY,
				instances  INTEGER NOT NULL DEFAULT 0,
				vcpu       INTEGER NOT NULL DEFAULT 0,
				memory_mib INTEGER NOT NULL DEFAULT 0,
				volumes    INTEGER NOT NULL DEFAULT 0,
				volume_gib INTEGER NOT NULL DEFAULT 0,
				updated_at TEXT    NOT NULL
			)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/quota", httpx.Wrap(m.log, m.handler.mine))
	mux.Handle("GET /v1/quotas", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/quotas/{projectID}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("PUT /v1/quotas/{projectID}", httpx.Wrap(m.log, m.handler.set))
	mux.Handle("DELETE /v1/quotas/{projectID}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) UseUsage(u Usage) {
	m.svc.usage = u
}

func (m *Module) UseProjects(p Projects) {
	m.svc.projects = p
}

func (m *Module) Admit(ctx context.Context, projectID string, claim Claim) error {
	return m.svc.admit(ctx, projectID, claim)
}
