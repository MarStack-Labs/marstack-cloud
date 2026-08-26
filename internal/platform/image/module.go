package image

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
	return "image"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "image",
			Index:  1,
			SQL: `CREATE TABLE images (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				kind       TEXT NOT NULL,
				arch       TEXT NOT NULL,
				source     TEXT NOT NULL,
				checksum   TEXT NOT NULL DEFAULT '',
				size_bytes INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL
			)`,
		},
		{
			Module: "image",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX images_name_unique ON images (name)`,
		},
		{
			Module: "image",
			Index:  3,
			SQL: `CREATE TABLE node_images (
				node_id     TEXT NOT NULL,
				image_id    TEXT NOT NULL,
				size_bytes  INTEGER NOT NULL DEFAULT 0,
				reported_at TEXT NOT NULL,
				PRIMARY KEY (node_id, image_id)
			)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/images", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/images", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/images/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/images/{id}", httpx.Wrap(m.log, m.handler.delete))
	mux.Handle("PUT /v1/nodes/{nodeID}/images", httpx.Wrap(m.log, m.handler.report))
}

func (m *Module) Resolve(ctx context.Context, nameOrID string) (Image, error) {
	return m.svc.resolve(ctx, nameOrID)
}

func (m *Module) List(ctx context.Context) ([]Placement, error) {
	return m.svc.list(ctx)
}
