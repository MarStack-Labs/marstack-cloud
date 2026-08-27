package project

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
	return "project"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "project",
			Index:  1,
			SQL: `CREATE TABLE projects (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				created_at TEXT NOT NULL
			)`,
		},
		{
			Module: "project",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX projects_name_unique ON projects (name)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/projects", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/projects", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/projects/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/projects/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) UseOccupancy(o Occupancy) {
	m.svc.occupancy = o
}

func (m *Module) EnsureDefault(ctx context.Context) (Project, error) {
	p, err := m.svc.ensureDefault(ctx)
	if err != nil {
		return Project{}, err
	}
	m.log.Info("default project ready", "project", p.Name, "id", p.ID)
	return p, nil
}

func (m *Module) Exists(ctx context.Context, id string) (bool, error) {
	return m.svc.exists(ctx, id)
}
