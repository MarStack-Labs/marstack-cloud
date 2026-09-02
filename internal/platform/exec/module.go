package exec

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

func (m *Module) Name() string {
	return "exec"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "exec",
			Index:  1,
			SQL: `CREATE TABLE execs (
				id              TEXT    PRIMARY KEY,
				project_id      TEXT    NOT NULL,
				instance_id     TEXT    NOT NULL,
				node_id         TEXT    NOT NULL DEFAULT '',
				isolation       TEXT    NOT NULL DEFAULT '',
				command         TEXT    NOT NULL DEFAULT '[]',
				timeout_seconds INTEGER NOT NULL DEFAULT 30,
				state           TEXT    NOT NULL,
				output          TEXT    NOT NULL DEFAULT '',
				truncated       INTEGER NOT NULL DEFAULT 0,
				exit_code       INTEGER,
				message         TEXT    NOT NULL DEFAULT '',
				created_at      TEXT    NOT NULL,
				taken_at        TEXT    NOT NULL DEFAULT '',
				ended_at        TEXT    NOT NULL DEFAULT ''
			)`,
		},
		{
			Module: "exec",
			Index:  2,
			SQL:    `CREATE INDEX execs_waiting ON execs (node_id, state, created_at)`,
		},
		{
			Module: "exec",
			Index:  3,
			SQL:    `CREATE INDEX execs_project ON execs (project_id, created_at)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/instances/{id}/exec", httpx.Wrap(m.log, m.handler.start))
	mux.Handle("GET /v1/execs", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/execs/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("GET /v1/nodes/{nodeID}/exec", httpx.Wrap(m.log, m.handler.take))
	mux.Handle("POST /v1/nodes/{nodeID}/execs/{execID}/result",
		httpx.Wrap(m.log, m.handler.finish))
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
	return m.svc.repo.trim(ctx, instanceID, 0)
}

func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Sweep(ctx)
		}
	}
}

func (m *Module) Sweep(ctx context.Context) {
	lost, err := m.svc.sweep(ctx)
	if err != nil {
		m.log.Warn("could not give up on abandoned commands", "error", err)
		return
	}
	if lost == 0 {
		return
	}
	m.log.Info("commands nobody picked up were given up on", "count", lost)
}
