package service

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
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
	svc := newService(newRepository(st), log, nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "service"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "service",
			Index:  1,
			SQL: `CREATE TABLE services (
				id               TEXT    PRIMARY KEY,
				project_id       TEXT    NOT NULL,
				name             TEXT    NOT NULL,
				replicas         INTEGER NOT NULL DEFAULT 0,
				isolation        TEXT    NOT NULL DEFAULT 'container',
				image            TEXT    NOT NULL DEFAULT '',
				iso              TEXT    NOT NULL DEFAULT '',
				kernel           TEXT    NOT NULL DEFAULT '',
				disk_gib         INTEGER NOT NULL DEFAULT 0,
				firewall_id      TEXT    NOT NULL DEFAULT '',
				command          TEXT    NOT NULL DEFAULT '[]',
				network_id       TEXT    NOT NULL DEFAULT '',
				restart_policy   TEXT    NOT NULL DEFAULT '',
				vcpu             INTEGER NOT NULL DEFAULT 0,
				memory_mib       INTEGER NOT NULL DEFAULT 0,
				placement_group  TEXT    NOT NULL DEFAULT '',
				placement_strict INTEGER NOT NULL DEFAULT 0,
				ssh_keys         TEXT    NOT NULL DEFAULT '[]',
				blocked          TEXT    NOT NULL DEFAULT '',
				created_at       TEXT    NOT NULL,
				updated_at       TEXT    NOT NULL
			)`,
		},
		{
			Module: "service",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX services_name_unique ON services (project_id, name)`,
		},
		{
			Module: "service",
			Index:  3,
			SQL: `CREATE TABLE service_members (
				service_id  TEXT NOT NULL,
				instance_id TEXT NOT NULL,
				created_at  TEXT NOT NULL,
				PRIMARY KEY (service_id, instance_id)
			)`,
		},
		{
			Module: "service",
			Index:  4,
			SQL: `CREATE INDEX service_members_instance_id
				ON service_members (instance_id)`,
		},
		{
			Module: "service",
			Index:  5,
			SQL:    `ALTER TABLE services ADD COLUMN node_selector TEXT NOT NULL DEFAULT '{}'`,
		},
		{
			Module: "service",
			Index:  6,
			SQL:    `ALTER TABLE services ADD COLUMN env TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "service",
			Index:  7,
			SQL:    `ALTER TABLE services ADD COLUMN env_key_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "service",
			Index:  8,
			SQL:    `ALTER TABLE services ADD COLUMN env_names TEXT NOT NULL DEFAULT '[]'`,
		},
		{
			Module: "service",
			Index:  9,
			SQL:    `ALTER TABLE services ADD COLUMN extra_networks TEXT NOT NULL DEFAULT '[]'`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/services", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/services", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/services/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("POST /v1/services/{id}/scale", httpx.Wrap(m.log, m.handler.scale))
	mux.Handle("DELETE /v1/services/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) UseSealing(ring *sealed.Keyring) {
	m.svc.sealing = ring
}

func (m *Module) UseWorkloads(workloads Workloads) {
	m.svc.workloads = workloads
}

func (m *Module) MembersOf(ctx context.Context, projectID, serviceID string) ([]string, error) {
	return m.svc.membersOf(ctx, projectID, serviceID)
}

func (m *Module) UseEvents(recorder events.Recorder) {
	m.svc.events = recorder
}

func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(ReconcileEvery)
	defer ticker.Stop()

	m.log.Info("services running", "every", ReconcileEvery.String())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Reconcile(ctx)
		}
	}
}

func (m *Module) Reconcile(ctx context.Context) {
	created, removed, err := m.svc.reconcile(ctx)
	if err != nil {
		m.log.Warn("could not reconcile services", "error", err)
		return
	}
	if created == 0 && removed == 0 {
		return
	}
	m.log.Info("services reconciled", "created", created, "removed", removed)
}
