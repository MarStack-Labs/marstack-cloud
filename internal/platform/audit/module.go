package audit

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

func New(st *store.Store, log *slog.Logger) *Module {
	svc := newService(newRepository(st), nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "audit"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "audit",
			Index:  1,
			SQL: `CREATE TABLE audit (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				at         TEXT NOT NULL,
				actor      TEXT NOT NULL DEFAULT '',
				role       TEXT NOT NULL DEFAULT '',
				method     TEXT NOT NULL,
				path       TEXT NOT NULL,
				status     INTEGER NOT NULL,
				request_id TEXT NOT NULL DEFAULT ''
			)`,
		},
		{
			Module: "audit",
			Index:  2,
			SQL:    `CREATE INDEX audit_at ON audit (at)`,
		},
		{
			Module: "audit",
			Index:  3,
			SQL:    `ALTER TABLE audit ADD COLUMN project_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "audit",
			Index:  4,
			SQL:    `ALTER TABLE audit ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/audit", httpx.Wrap(m.log, m.handler.list))
}

func (m *Module) Record(ctx context.Context, record Record) {
	if err := m.svc.record(ctx, record); err != nil {
		m.log.Error("could not write an audit entry",
			"actor", record.Actor, "method", record.Method, "path", record.Path, "error", err)
	}
}

func (m *Module) Count(ctx context.Context) (int, error) {
	return m.svc.count(ctx)
}
