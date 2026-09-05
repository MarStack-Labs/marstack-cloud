package acme

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
	return "acme"
}

func (m *Module) UseBalancers(b Balancers) {
	m.svc.balancers = b
}

func (m *Module) UseEvents(recorder events.Recorder) {
	m.svc.events = recorder
}

func (m *Module) UseDirectory(directory, contact string) {
	m.svc.directory = directory
	m.svc.contact = contact
}

func (m *Module) UseClock(now func() time.Time) {
	if now == nil {
		return
	}
	m.svc.now = now
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "acme",
			Index:  1,
			SQL: `CREATE TABLE acme_orders (
				id          TEXT PRIMARY KEY,
				project_id  TEXT NOT NULL,
				balancer_id TEXT NOT NULL,
				names       TEXT NOT NULL DEFAULT '[]',
				state       TEXT NOT NULL,
				message     TEXT NOT NULL DEFAULT '',
				order_url   TEXT NOT NULL DEFAULT '',
				key_pem     TEXT NOT NULL DEFAULT '',
				challenges  TEXT NOT NULL DEFAULT '[]',
				expires_at  TEXT NOT NULL DEFAULT '',
				issued_at   TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL,
				updated_at  TEXT NOT NULL
			)`,
		},
		{
			Module: "acme",
			Index:  2,
			SQL: `CREATE UNIQUE INDEX acme_orders_balancer_unique
				ON acme_orders (balancer_id)`,
		},
		{
			Module: "acme",
			Index:  3,
			SQL: `CREATE TABLE acme_account (
				id      INTEGER PRIMARY KEY,
				key_pem TEXT NOT NULL,
				url     TEXT NOT NULL
			)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/balancers/{id}/certificate/acme",
		httpx.Wrap(m.log, m.handler.start))
	mux.Handle("GET /v1/balancers/{id}/certificate/acme", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/balancers/{id}/certificate/acme", httpx.Wrap(m.log, m.handler.stop))
	mux.Handle("GET /v1/certificates", httpx.Wrap(m.log, m.handler.list))

	mux.Handle("GET /v1/nodes/{nodeID}/acme-challenges",
		httpx.Wrap(m.log, m.handler.listForNode))
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
		m.log.Warn("could not work through the certificate orders", "error", err)
		return
	}
	if moved > 0 {
		m.log.Info("certificate orders moved", "count", moved)
	}
}
