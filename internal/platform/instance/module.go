package instance

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

func (m *Module) PendingPlacement(ctx context.Context) ([]Instance, error) {
	return m.svc.pendingPlacement(ctx)
}

func (m *Module) AssignedCounts(ctx context.Context) (map[string]int, error) {
	return m.svc.assignedCounts(ctx)
}

func (m *Module) Assign(ctx context.Context, instanceID, nodeID string) error {
	return m.svc.assign(ctx, instanceID, nodeID)
}

func (m *Module) Name() string {
	return "instance"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "instance",
			Index:  1,
			SQL: `CREATE TABLE instances (
				id             TEXT    PRIMARY KEY,
				name           TEXT    NOT NULL,
				isolation      TEXT    NOT NULL,
				image          TEXT    NOT NULL,
				vcpu           INTEGER NOT NULL,
				memory_mib     INTEGER NOT NULL,
				desired_state  TEXT    NOT NULL,
				observed_state TEXT    NOT NULL,
				node_id        TEXT,
				created_at     TEXT    NOT NULL,
				updated_at     TEXT    NOT NULL
			)`,
		},
		{
			Module: "instance",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX instances_name_unique ON instances (name)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/instances", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/instances", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/instances/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/instances/{id}", httpx.Wrap(m.log, m.handler.delete))
	mux.Handle("POST /v1/instances/{id}/start", httpx.Wrap(m.log, m.handler.start))
	mux.Handle("POST /v1/instances/{id}/stop", httpx.Wrap(m.log, m.handler.stop))
}
