package registry

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
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
	return "registry"
}

func (m *Module) UseKeys(ring *sealed.Keyring) {
	m.svc.keys = ring
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
			Module: "registry",
			Index:  1,
			SQL: `CREATE TABLE registry_credentials (
				id         TEXT PRIMARY KEY,
				host       TEXT NOT NULL,
				username   TEXT NOT NULL,
				sealed     TEXT NOT NULL,
				key_id     TEXT NOT NULL,
				created_at TEXT NOT NULL
			)`,
		},
		{
			Module: "registry",
			Index:  2,
			SQL: `CREATE UNIQUE INDEX registry_credentials_host_unique
				ON registry_credentials (host)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/registries", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/registries", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("DELETE /v1/registries/{id}", httpx.Wrap(m.log, m.handler.delete))

	mux.Handle("GET /v1/nodes/{nodeID}/registries",
		httpx.Wrap(m.log, m.handler.listForNode))
}
