package webhook

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
	return "webhook"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "webhook",
			Index:  1,
			SQL: `CREATE TABLE webhooks (
				id         TEXT    PRIMARY KEY,
				project_id TEXT    NOT NULL,
				name       TEXT    NOT NULL,
				url        TEXT    NOT NULL,
				secret     TEXT    NOT NULL,
				kinds      TEXT    NOT NULL DEFAULT '[]',
				active     INTEGER NOT NULL DEFAULT 1,
				created_at TEXT    NOT NULL
			)`,
		},
		{
			Module: "webhook",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX webhooks_name_unique ON webhooks (project_id, name)`,
		},
		{
			Module: "webhook",
			Index:  3,
			SQL: `CREATE TABLE webhook_deliveries (
				id              TEXT    PRIMARY KEY,
				subscription_id TEXT    NOT NULL,
				event_id        INTEGER NOT NULL,
				kind            TEXT    NOT NULL,
				subject         TEXT    NOT NULL DEFAULT '',
				state           TEXT    NOT NULL,
				attempts        INTEGER NOT NULL DEFAULT 0,
				last_error      TEXT    NOT NULL DEFAULT '',
				next_attempt_at TEXT    NOT NULL,
				created_at      TEXT    NOT NULL,
				updated_at      TEXT    NOT NULL
			)`,
		},
		{
			Module: "webhook",
			Index:  4,
			SQL: `CREATE INDEX webhook_deliveries_due
				ON webhook_deliveries (state, next_attempt_at)`,
		},
		{
			Module: "webhook",
			Index:  5,
			SQL: `CREATE INDEX webhook_deliveries_subscription
				ON webhook_deliveries (subscription_id)`,
		},
		{
			Module: "webhook",
			Index:  6,
			SQL: `CREATE TABLE webhook_cursor (
				id            INTEGER PRIMARY KEY,
				last_event_id INTEGER NOT NULL
			)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/webhooks", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/webhooks", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/webhooks/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("GET /v1/webhooks/{id}/deliveries", httpx.Wrap(m.log, m.handler.deliveries))
	mux.Handle("POST /v1/webhooks/{id}/pause", httpx.Wrap(m.log, m.handler.pause))
	mux.Handle("POST /v1/webhooks/{id}/resume", httpx.Wrap(m.log, m.handler.resume))
	mux.Handle("DELETE /v1/webhooks/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) AllowLoopbackBecauseThisIsATest() {
	m.svc.http = newClient(func(string) error { return nil })
}

func (m *Module) UseEvents(events Events) {
	m.svc.events = events
}

func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(FanOutEvery)
	defer ticker.Stop()

	m.log.Info("webhooks running", "every", FanOutEvery.String())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Pump(ctx)
		}
	}
}

func (m *Module) Deliver(ctx context.Context) {
	if _, err := m.svc.drain(ctx); err != nil {
		m.log.Warn("could not deliver webhooks", "error", err)
	}
}

func (m *Module) Pump(ctx context.Context) {
	queued, err := m.svc.fanOut(ctx)
	if err != nil {
		m.log.Warn("could not fan out events to webhooks", "error", err)
	}

	sent, err := m.svc.drain(ctx)
	if err != nil {
		m.log.Warn("could not deliver webhooks", "error", err)
	}
	if queued == 0 && sent == 0 {
		return
	}
	m.log.Info("webhooks pumped", "queued", queued, "delivered", sent)
}
