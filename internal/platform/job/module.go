package job

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
	return "job"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "job",
			Index:  1,
			SQL: `CREATE TABLE jobs (
				id            TEXT    PRIMARY KEY,
				project_id    TEXT    NOT NULL,
				name          TEXT    NOT NULL,
				template      TEXT    NOT NULL DEFAULT '{}',
				env           TEXT    NOT NULL DEFAULT '',
				files         TEXT    NOT NULL DEFAULT '',
				seal_key_id   TEXT    NOT NULL DEFAULT '',
				every_seconds INTEGER NOT NULL DEFAULT 0,
				retries       INTEGER NOT NULL DEFAULT 0,
				keep_runs     INTEGER NOT NULL DEFAULT 10,
				paused        INTEGER NOT NULL DEFAULT 0,
				next_at       TEXT    NOT NULL DEFAULT '',
				last_at       TEXT    NOT NULL DEFAULT '',
				created_at    TEXT    NOT NULL,
				updated_at    TEXT    NOT NULL
			)`,
		},
		{
			Module: "job",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX jobs_name_unique ON jobs (project_id, name)`,
		},
		{
			Module: "job",
			Index:  3,
			SQL: `CREATE TABLE job_runs (
				id          TEXT    PRIMARY KEY,
				job_id      TEXT    NOT NULL,
				instance_id TEXT    NOT NULL DEFAULT '',
				attempt     INTEGER NOT NULL DEFAULT 1,
				state       TEXT    NOT NULL,
				message     TEXT    NOT NULL DEFAULT '',
				exit_code   INTEGER,
				started_at  TEXT    NOT NULL,
				ended_at    TEXT    NOT NULL DEFAULT ''
			)`,
		},
		{
			Module: "job",
			Index:  4,
			SQL:    `CREATE INDEX job_runs_job ON job_runs (job_id, started_at)`,
		},
		{
			Module: "job",
			Index:  5,
			SQL:    `CREATE INDEX jobs_next_at ON jobs (next_at)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/jobs", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/jobs", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/jobs/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("GET /v1/jobs/{id}/runs", httpx.Wrap(m.log, m.handler.runs))
	mux.Handle("POST /v1/jobs/{id}/run", httpx.Wrap(m.log, m.handler.trigger))
	mux.Handle("POST /v1/jobs/{id}/pause", httpx.Wrap(m.log, m.handler.pause))
	mux.Handle("POST /v1/jobs/{id}/resume", httpx.Wrap(m.log, m.handler.resume))
	mux.Handle("DELETE /v1/jobs/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) UseWorkloads(workloads Workloads) {
	m.svc.workloads = workloads
}

func (m *Module) UseNetworks(networks Registry) {
	m.svc.networks = networks
}

func (m *Module) UseFirewalls(firewalls Registry) {
	m.svc.firewalls = firewalls
}

func (m *Module) UseEvents(recorder events.Recorder) {
	m.svc.events = recorder
}

func (m *Module) UseSealing(ring *sealed.Keyring) {
	m.svc.sealing = ring
}

func (m *Module) UseClock(now func() time.Time) {
	if now == nil {
		return
	}
	m.svc.now = now
}

func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()

	m.log.Info("jobs running", "every", SweepInterval.String())

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
	started, finished, err := m.svc.sweep(ctx)
	if err != nil {
		m.log.Warn("could not sweep jobs", "error", err)
		return
	}
	if started == 0 && finished == 0 {
		return
	}
	m.log.Info("jobs swept", "started", started, "finished", finished)
}
