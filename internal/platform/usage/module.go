package usage

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
	svc := newService(newRepository(st), log, nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) UseClock(now func() time.Time) {
	if now == nil {
		return
	}
	m.svc.now = now
}

func (m *Module) Name() string {
	return "usage"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "usage",
			Index:  1,
			SQL: `CREATE TABLE node_usage (
				node_id         TEXT PRIMARY KEY,
				cpu_percent     REAL NOT NULL DEFAULT 0,
				memory_used_mib INTEGER NOT NULL DEFAULT 0,
				memory_mib      INTEGER NOT NULL DEFAULT 0,
				reported_at     TEXT NOT NULL
			)`,
		},
		{
			Module: "usage",
			Index:  2,
			SQL: `CREATE TABLE instance_usage (
				instance_id     TEXT PRIMARY KEY,
				node_id         TEXT NOT NULL,
				cpu_percent     REAL NOT NULL DEFAULT 0,
				memory_used_mib INTEGER NOT NULL DEFAULT 0,
				reported_at     TEXT NOT NULL
			)`,
		},
		{
			Module: "usage",
			Index:  3,
			SQL:    `CREATE INDEX instance_usage_node_id ON instance_usage (node_id)`,
		},
		{
			Module: "usage",
			Index:  4,
			SQL: `CREATE TABLE usage_history (
				subject     TEXT    NOT NULL,
				bucket      INTEGER NOT NULL,
				samples     INTEGER NOT NULL DEFAULT 0,
				cpu_sum     REAL    NOT NULL DEFAULT 0,
				cpu_peak    REAL    NOT NULL DEFAULT 0,
				memory_sum  INTEGER NOT NULL DEFAULT 0,
				memory_peak INTEGER NOT NULL DEFAULT 0,
				memory_mib  INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (subject, bucket)
			)`,
		},
		{
			Module: "usage",
			Index:  5,
			SQL:    `CREATE INDEX usage_history_bucket ON usage_history (bucket)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/usage", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/usage/nodes", httpx.Wrap(m.log, m.handler.listNodes))
	mux.Handle("GET /v1/usage/history", httpx.Wrap(m.log, m.handler.history))
	mux.Handle("GET /v1/usage/nodes/history", httpx.Wrap(m.log, m.handler.nodeHistory))
	mux.Handle("PUT /v1/nodes/{nodeID}/usage", httpx.Wrap(m.log, m.handler.report))
}

type Workloads interface {
	IDsIn(ctx context.Context, projectID string) ([]string, error)
}

func (m *Module) UseWorkloads(workloads Workloads) {
	m.svc.workloads = workloads
}

func (m *Module) Nodes(ctx context.Context) ([]NodeSample, error) {
	return m.svc.nodes(ctx)
}

func (m *Module) Instances(ctx context.Context) ([]InstanceSample, error) {
	return m.svc.repo.instances(ctx)
}

func (m *Module) Forget(ctx context.Context, nodeID string) error {
	return m.svc.forget(ctx, nodeID)
}
