package balancer

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
	svc := newService(newRepository(st), nil, nil, nil)
	return &Module{
		log:     log,
		svc:     svc,
		handler: &handler{svc: svc},
	}
}

func (m *Module) Name() string {
	return "balancer"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "balancer",
			Index:  1,
			SQL: `CREATE TABLE balancers (
				id          TEXT PRIMARY KEY,
				project_id  TEXT    NOT NULL,
				name        TEXT    NOT NULL,
				protocol    TEXT    NOT NULL,
				listen_port INTEGER NOT NULL,
				target_port INTEGER NOT NULL,
				algorithm   TEXT    NOT NULL,
				created_at  TEXT    NOT NULL
			)`,
		},
		{
			Module: "balancer",
			Index:  2,
			SQL: `CREATE UNIQUE INDEX balancers_name_unique
				ON balancers (project_id, name)`,
		},
		{
			Module: "balancer",
			Index:  3,
			SQL: `CREATE UNIQUE INDEX balancers_listen_port_unique
				ON balancers (protocol, listen_port)`,
		},
		{
			Module: "balancer",
			Index:  4,
			SQL: `CREATE TABLE balancer_backends (
				balancer_id TEXT NOT NULL,
				instance_id TEXT NOT NULL,
				added_at    TEXT NOT NULL,
				PRIMARY KEY (balancer_id, instance_id)
			)`,
		},
		{
			Module: "balancer",
			Index:  5,
			SQL: `CREATE INDEX balancer_backends_instance_id
				ON balancer_backends (instance_id)`,
		},
		{
			Module: "balancer",
			Index:  6,
			SQL:    `ALTER TABLE balancers ADD COLUMN check_kind TEXT NOT NULL DEFAULT 'none'`,
		},
		{
			Module: "balancer",
			Index:  7,
			SQL:    `ALTER TABLE balancers ADD COLUMN check_path TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "balancer",
			Index:  8,
			SQL:    `ALTER TABLE balancers ADD COLUMN rise INTEGER NOT NULL DEFAULT 2`,
		},
		{
			Module: "balancer",
			Index:  9,
			SQL:    `ALTER TABLE balancers ADD COLUMN fall INTEGER NOT NULL DEFAULT 2`,
		},
		{
			Module: "balancer",
			Index:  10,
			SQL: `CREATE TABLE balancer_health (
				balancer_id TEXT    NOT NULL,
				instance_id TEXT    NOT NULL,
				healthy     INTEGER NOT NULL,
				reason      TEXT    NOT NULL,
				checked_at  TEXT    NOT NULL,
				PRIMARY KEY (balancer_id, instance_id)
			)`,
		},
		{
			Module: "balancer",
			Index:  11,
			SQL:    `ALTER TABLE balancers ADD COLUMN service_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "balancer",
			Index:  12,
			SQL:    `ALTER TABLE balancers ADD COLUMN tls_material TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "balancer",
			Index:  13,
			SQL:    `ALTER TABLE balancers ADD COLUMN tls_key_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "balancer",
			Index:  14,
			SQL:    `ALTER TABLE balancers ADD COLUMN tls_subject TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "balancer",
			Index:  15,
			SQL:    `ALTER TABLE balancers ADD COLUMN tls_expires_at TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "balancer",
			Index:  16,
			SQL:    `ALTER TABLE balancers ADD COLUMN family TEXT NOT NULL DEFAULT ''`,
		},
		{
			Module: "balancer",
			Index:  17,
			SQL: `CREATE TABLE balancer_routes (
				balancer_id TEXT NOT NULL,
				host        TEXT NOT NULL DEFAULT '',
				path        TEXT NOT NULL DEFAULT '',
				service_id  TEXT NOT NULL,
				PRIMARY KEY (balancer_id, host, path)
			)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/balancers", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/balancers", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/balancers/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("DELETE /v1/balancers/{id}", httpx.Wrap(m.log, m.handler.delete))
	mux.Handle("POST /v1/balancers/{id}/backends", httpx.Wrap(m.log, m.handler.addBackend))
	mux.Handle("DELETE /v1/balancers/{id}/backends/{instanceID}",
		httpx.Wrap(m.log, m.handler.removeBackend))

	mux.Handle("PUT /v1/balancers/{id}/routes", httpx.Wrap(m.log, m.handler.setRoutes))

	mux.Handle("PUT /v1/balancers/{id}/certificate", httpx.Wrap(m.log, m.handler.setCertificate))
	mux.Handle("DELETE /v1/balancers/{id}/certificate",
		httpx.Wrap(m.log, m.handler.removeCertificate))

	mux.Handle("GET /v1/nodes/{nodeID}/balancers", httpx.Wrap(m.log, m.handler.listForNode))
	mux.Handle("PUT /v1/nodes/{nodeID}/balancers/health", httpx.Wrap(m.log, m.handler.reportHealth))
}

func (m *Module) HostsOf(ctx context.Context, id, projectID string) ([]string, error) {
	return m.svc.hostsOf(ctx, id, projectID)
}

func (m *Module) AttachCertificate(
	ctx context.Context, id, projectID, certPEM, keyPEM string,
) error {
	_, err := m.svc.setCertificate(ctx, id, projectID, certPEM, keyPEM)
	return err
}

func (m *Module) UseSealing(ring *sealed.Keyring) {
	m.svc.sealing = ring
}

func (m *Module) UseMembers(members Members) {
	m.svc.members = members
}

func (m *Module) UsePorts(ports Ports) {
	m.svc.ports = ports
}

func (m *Module) UseServices(services Services) {
	m.svc.services = services
}

func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(CertificateSweep)
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
	told, err := m.svc.sweepCertificates(ctx)
	if err != nil {
		m.log.Warn("could not check the balancer certificates", "error", err)
		return
	}
	if told > 0 {
		m.log.Info("certificates need attention", "balancers", told)
	}
}

func (m *Module) UseEvents(recorder events.Recorder) {
	m.svc.events = recorder
}

func (m *Module) ReleaseInstance(ctx context.Context, instanceID string) error {
	return m.svc.releaseInstance(ctx, instanceID)
}

func (m *Module) ListenPortTaken(ctx context.Context, protocol string, port int) (bool, error) {
	return m.svc.listenPortTaken(ctx, protocol, port)
}
