package keypair

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
	return "keypair"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "keypair",
			Index:  1,
			SQL: `CREATE TABLE ssh_keys (
				id          TEXT PRIMARY KEY,
				project_id  TEXT NOT NULL,
				name        TEXT NOT NULL,
				public_key  TEXT NOT NULL,
				fingerprint TEXT NOT NULL,
				kind        TEXT NOT NULL,
				comment     TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL
			)`,
		},
		{
			Module: "keypair",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX ssh_keys_name_unique ON ssh_keys (project_id, name)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/keys", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/keys", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("DELETE /v1/keys/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) Resolve(
	ctx context.Context, projectID string, names []string,
) ([]string, error) {
	return m.svc.resolve(ctx, projectID, names)
}
