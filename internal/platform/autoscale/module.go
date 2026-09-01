package autoscale

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
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "autoscale"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "autoscale",
			Index:  1,
			SQL: `CREATE TABLE autoscalers (
				id           TEXT    PRIMARY KEY,
				project_id   TEXT    NOT NULL,
				service_id   TEXT    NOT NULL,
				min_replicas INTEGER NOT NULL DEFAULT 1,
				max_replicas INTEGER NOT NULL DEFAULT 1,
				target_cpu   INTEGER NOT NULL DEFAULT 70,
				last_at      TEXT    NOT NULL DEFAULT '',
				last_reason  TEXT    NOT NULL DEFAULT '',
				created_at   TEXT    NOT NULL,
				updated_at   TEXT    NOT NULL
			)`,
		},
		{
			Module: "autoscale",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX autoscalers_service ON autoscalers (service_id)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("PUT /v1/services/{id}/autoscale", httpx.Wrap(m.log, m.handler.set))
	mux.Handle("GET /v1/services/{id}/autoscale", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/services/{id}/autoscale", httpx.Wrap(m.log, m.handler.clear))
	mux.Handle("GET /v1/autoscalers", httpx.Wrap(m.log, m.handler.list))
}

func (m *Module) UseServices(services Services) {
	m.svc.services = services
}

func (m *Module) UseLoad(load Load) {
	m.svc.load = load
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

func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()

	m.log.Info("autoscalers running", "every", SweepInterval.String())

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
	changed, err := m.svc.sweep(ctx)
	if err != nil {
		m.log.Warn("could not sweep autoscalers", "error", err)
		return
	}
	if changed == 0 {
		return
	}
	m.log.Info("autoscalers swept", "changed", changed)
}
