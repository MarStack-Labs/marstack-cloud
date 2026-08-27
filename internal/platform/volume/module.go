package volume

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

func New(st *store.Store, instances Instances, log *slog.Logger) *Module {
	svc := newService(newRepository(st), instances, nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) UseBackups(b Backups) {
	m.svc.backups = b
}

func (m *Module) Find(ctx context.Context, nameOrID, projectID string) (Volume, error) {
	return m.svc.resolveIn(ctx, nameOrID, projectID)
}

func (m *Module) Name() string {
	return "volume"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "volume",
			Index:  1,
			SQL: `CREATE TABLE volumes (
				id          TEXT PRIMARY KEY,
				name        TEXT NOT NULL,
				size_gib    INTEGER NOT NULL,
				node_id     TEXT NOT NULL DEFAULT '',
				instance_id TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL,
				updated_at  TEXT NOT NULL
			)`,
		},
		{
			Module: "volume",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX volumes_name_unique ON volumes (name)`,
		},
		{
			Module: "volume",
			Index:  3,
			SQL:    `CREATE INDEX volumes_node_id ON volumes (node_id)`,
		},
		{
			Module: "volume",
			Index:  4,
			SQL:    `ALTER TABLE volumes ADD COLUMN restore_from TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "volume",
			Index:  5,
			SQL: `CREATE TABLE snapshots (
				id         TEXT PRIMARY KEY,
				volume_id  TEXT NOT NULL,
				name       TEXT NOT NULL,
				state      TEXT NOT NULL,
				message    TEXT NOT NULL DEFAULT '',
				size_bytes INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL
			)`,
		},
		{
			Module: "volume",
			Index:  6,
			SQL:    `CREATE UNIQUE INDEX snapshots_name_unique ON snapshots (volume_id, name)`,
		},
		{
			Module: "volume",
			Index:  7,
			SQL:    `ALTER TABLE volumes ADD COLUMN project_id TEXT NOT NULL DEFAULT 'prj-default'`,
		},
		{
			Module: "volume",
			Index:  8,
			SQL:    `DROP INDEX volumes_name_unique`,
		},
		{
			Module: "volume",
			Index:  9,
			SQL:    `CREATE UNIQUE INDEX volumes_name_unique ON volumes (project_id, name)`,
		},
		{
			Module: "volume",
			Index:  10,
			SQL:    `ALTER TABLE volumes ADD COLUMN backup_id TEXT NOT NULL DEFAULT ''`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/volumes", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/volumes", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/volumes/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/volumes/{id}", httpx.Wrap(m.log, m.handler.delete))
	mux.Handle("POST /v1/volumes/{id}/attach", httpx.Wrap(m.log, m.handler.attach))
	mux.Handle("POST /v1/volumes/{id}/detach", httpx.Wrap(m.log, m.handler.detach))

	mux.Handle("POST /v1/volumes/{id}/snapshots", httpx.Wrap(m.log, m.handler.snapshot))
	mux.Handle("GET /v1/volumes/{id}/snapshots", httpx.Wrap(m.log, m.handler.listSnapshots))
	mux.Handle("GET /v1/snapshots", httpx.Wrap(m.log, m.handler.listSnapshots))
	mux.Handle("DELETE /v1/snapshots/{id}", httpx.Wrap(m.log, m.handler.deleteSnapshot))
	mux.Handle("POST /v1/snapshots/{id}/restore", httpx.Wrap(m.log, m.handler.restore))

	mux.Handle("GET /v1/nodes/{nodeID}/volumes", httpx.Wrap(m.log, m.handler.listForNode))
	mux.Handle("PUT /v1/nodes/{nodeID}/volumes", httpx.Wrap(m.log, m.handler.report))
}

func (m *Module) ReleaseInstance(ctx context.Context, instanceID string) error {
	return m.svc.releaseInstance(ctx, instanceID)
}
