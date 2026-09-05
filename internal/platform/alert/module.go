package alert

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
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
	return "alert"
}

func (m *Module) UseLoad(load Load) {
	m.svc.load = load
}

func (m *Module) UseWorkloads(workloads Workloads) {
	m.svc.workloads = workloads
}

func (m *Module) UseEvents(recorder events.Recorder) {
	m.svc.events = recorder
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
			Module: "alert",
			Index:  1,
			SQL: `CREATE TABLE alerts (
				id          TEXT PRIMARY KEY,
				project_id  TEXT NOT NULL,
				name        TEXT NOT NULL,
				instance_id TEXT NOT NULL,
				metric      TEXT NOT NULL,
				comparison  TEXT NOT NULL,
				threshold   REAL NOT NULL,
				for_seconds INTEGER NOT NULL,
				state       TEXT NOT NULL,
				since       TEXT NOT NULL,
				last_value  REAL NOT NULL DEFAULT 0,
				message     TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL,
				updated_at  TEXT NOT NULL
			)`,
		},
		{
			Module: "alert",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX alerts_name_unique ON alerts (project_id, name)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/alerts", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/alerts", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/alerts/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/alerts/{id}", httpx.Wrap(m.log, m.handler.delete))
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
	moved, err := m.svc.sweep(ctx)
	if err != nil {
		m.log.Warn("could not work through the alerts", "error", err)
		return
	}
	if moved > 0 {
		m.log.Info("alerts changed", "count", moved)
	}
}
