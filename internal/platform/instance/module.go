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

type Networks interface {
	DefaultNetworkID(ctx context.Context) (string, error)
	ReleaseAddress(ctx context.Context, instanceID string) error
}

func New(st *store.Store, log *slog.Logger, networks Networks) *Module {
	svc := newService(newRepository(st), nil)
	svc.networks = networks
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) All(ctx context.Context) ([]Instance, error) {
	return m.svc.list(ctx)
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
		{
			Module: "instance",
			Index:  3,
			SQL:    `ALTER TABLE instances ADD COLUMN observed_message TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "instance",
			Index:  4,
			SQL:    `CREATE INDEX instances_node_id ON instances (node_id)`,
		},
		{
			Module: "instance",
			Index:  5,
			SQL:    `ALTER TABLE instances ADD COLUMN command TEXT NOT NULL DEFAULT '[]'`,
		},
		{
			Module: "instance",
			Index:  6,
			SQL:    `ALTER TABLE instances ADD COLUMN network_id TEXT NOT NULL DEFAULT ''`,
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

	mux.Handle("GET /v1/nodes/{nodeID}/instances", httpx.Wrap(m.log, m.handler.listForNode))
	mux.Handle("PUT /v1/nodes/{nodeID}/instances/{instanceID}/status", httpx.Wrap(m.log, m.handler.reportStatus))
}
