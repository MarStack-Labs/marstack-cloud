package backup

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

func New(st *store.Store, dataDir string, log *slog.Logger) (*Module, error) {
	v, err := openVault(dataDir)
	if err != nil {
		return nil, err
	}

	svc := newService(newRepository(st), v, nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}, nil
}

func (m *Module) Name() string {
	return "backup"
}

func (m *Module) Close() error {
	return m.svc.vault.close()
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "backup",
			Index:  1,
			SQL: `CREATE TABLE backups (
				id         TEXT    PRIMARY KEY,
				project_id TEXT    NOT NULL,
				volume_id  TEXT    NOT NULL,
				node_id    TEXT    NOT NULL,
				name       TEXT    NOT NULL,
				state      TEXT    NOT NULL,
				message    TEXT    NOT NULL DEFAULT '',
				size_bytes INTEGER NOT NULL DEFAULT 0,
				checksum   TEXT    NOT NULL DEFAULT '',
				created_at TEXT    NOT NULL,
				updated_at TEXT    NOT NULL
			)`,
		},
		{
			Module: "backup",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX backups_name_unique ON backups (volume_id, name)`,
		},
		{
			Module: "backup",
			Index:  3,
			SQL:    `CREATE INDEX backups_node_state ON backups (node_id, state)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/volumes/{id}/backups", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/volumes/{id}/backups", httpx.Wrap(m.log, m.handler.listForVolume))
	mux.Handle("GET /v1/backups", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/backups/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/backups/{id}", httpx.Wrap(m.log, m.handler.delete))

	mux.Handle("GET /v1/nodes/{nodeID}/backups", httpx.Wrap(m.log, m.handler.listForNode))
	mux.Handle("PUT /v1/nodes/{nodeID}/backups/{id}/content", httpx.Wrap(m.log, m.handler.upload))
	mux.Handle("POST /v1/nodes/{nodeID}/backups/{id}/failure", httpx.Wrap(m.log, m.handler.fail))
	mux.Handle("GET /v1/nodes/{nodeID}/backups/{id}/content", httpx.Wrap(m.log, m.handler.download))
}

func (m *Module) UseVolumes(v Volumes) {
	m.svc.volumes = v
}

func (m *Module) Restorable(ctx context.Context, id, projectID string) (int64, error) {
	return m.svc.restorable(ctx, id, projectID)
}
