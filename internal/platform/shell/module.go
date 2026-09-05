package shell

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
	return &Module{log: log, svc: svc, handler: &handler{svc: svc}}
}

func (m *Module) Name() string {
	return "shell"
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
	return m.svc.ReleaseInstance(ctx, instanceID)
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "shell",
			Index:  1,
			SQL: `CREATE TABLE shell_sessions (
				id          TEXT PRIMARY KEY,
				project_id  TEXT NOT NULL,
				instance_id TEXT NOT NULL,
				node_id     TEXT NOT NULL,
				isolation   TEXT NOT NULL,
				command     TEXT NOT NULL DEFAULT '[]',
				state       TEXT NOT NULL,
				message     TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL,
				taken_at    TEXT NOT NULL DEFAULT '',
				ended_at    TEXT NOT NULL DEFAULT ''
			)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/instances/{id}/shell", httpx.Wrap(m.log, m.handler.start))
	mux.Handle("GET /v1/shells", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/shells/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/shells/{id}", httpx.Wrap(m.log, m.handler.closeSession))
	mux.Handle("GET /v1/shells/{id}/output", httpx.Wrap(m.log, m.handler.clientOutput))
	mux.Handle("POST /v1/shells/{id}/input", httpx.Wrap(m.log, m.handler.clientInput))

	mux.Handle("GET /v1/nodes/{nodeID}/shell", httpx.Wrap(m.log, m.handler.take))
	mux.Handle("POST /v1/nodes/{nodeID}/shells/{execID}/output",
		httpx.Wrap(m.log, m.handler.nodeOutput))
	mux.Handle("GET /v1/nodes/{nodeID}/shells/{execID}/input",
		httpx.Wrap(m.log, m.handler.nodeInput))
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
	closed, err := m.svc.sweep(ctx)
	if err != nil {
		m.log.Warn("could not sweep the shell sessions", "error", err)
		return
	}
	if closed > 0 {
		m.log.Info("shell sessions nobody took were closed", "count", closed)
	}
}
