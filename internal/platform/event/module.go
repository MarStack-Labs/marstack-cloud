package event

import (
	"context"
	"log/slog"
	"net/http"

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
	return "event"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "event",
			Index:  1,
			SQL: `CREATE TABLE events (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				at         TEXT NOT NULL,
				project_id TEXT NOT NULL DEFAULT '',
				kind       TEXT NOT NULL,
				subject    TEXT NOT NULL DEFAULT '',
				node_id    TEXT NOT NULL DEFAULT '',
				message    TEXT NOT NULL DEFAULT '',
				severity   TEXT NOT NULL DEFAULT 'info'
			)`,
		},
		{
			Module: "event",
			Index:  2,
			SQL:    `CREATE INDEX events_subject ON events (subject)`,
		},
		{
			Module: "event",
			Index:  3,
			SQL:    `CREATE INDEX events_project_id ON events (project_id)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/events", httpx.Wrap(m.log, m.handler.list))
}

func (m *Module) Record(ctx context.Context, entry events.Entry) {
	m.svc.record(ctx, entry)
}

func (m *Module) Since(ctx context.Context, afterID int64, limit int) ([]Entry, error) {
	return m.svc.since(ctx, afterID, limit)
}

func (m *Module) NewestID(ctx context.Context) (int64, error) {
	return m.svc.newestID(ctx)
}

func (m *Module) Count(ctx context.Context) (int, error) {
	return m.svc.count(ctx)
}
