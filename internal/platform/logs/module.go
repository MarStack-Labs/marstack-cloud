package logs

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

func (m *Module) Name() string {
	return "logs"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "logs",
			Index:  1,
			SQL: `CREATE TABLE instance_logs (
				seq         INTEGER PRIMARY KEY AUTOINCREMENT,
				instance_id TEXT NOT NULL,
				text        TEXT NOT NULL,
				received_at TEXT NOT NULL
			)`,
		},
		{
			Module: "logs",
			Index:  2,
			SQL:    `CREATE INDEX instance_logs_instance ON instance_logs (instance_id, seq)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("PUT /v1/nodes/{nodeID}/instances/{instanceID}/logs",
		httpx.Wrap(m.log, m.handler.report))
	mux.Handle("GET /v1/instances/{id}/logs", httpx.Wrap(m.log, m.handler.tail))
}

func (m *Module) UseInstances(instances Instances) {
	m.svc.instances = instances
}

func (m *Module) UseClock(now func() time.Time) {
	if now == nil {
		return
	}
	m.svc.now = now
}

func (m *Module) ReleaseInstance(ctx context.Context, instanceID string) error {
	return m.svc.forget(ctx, instanceID)
}
