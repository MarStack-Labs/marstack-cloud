package user

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
	return "user"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "user",
			Index:  1,
			SQL: `CREATE TABLE users (
				id         TEXT    PRIMARY KEY,
				email      TEXT    NOT NULL,
				name       TEXT    NOT NULL,
				role       TEXT    NOT NULL,
				project_id TEXT    NOT NULL DEFAULT '',
				disabled   INTEGER NOT NULL DEFAULT 0,
				password   TEXT    NOT NULL,
				created_at TEXT    NOT NULL,
				updated_at TEXT    NOT NULL
			)`,
		},
		{
			Module: "user",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX users_email ON users (email)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/login", httpx.Wrap(m.log, m.handler.login))
	mux.Handle("POST /v1/users", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/users", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("GET /v1/users/{id}", httpx.Wrap(m.log, m.handler.get))
	mux.Handle("PUT /v1/users/{id}/password", httpx.Wrap(m.log, m.handler.setPassword))
	mux.Handle("POST /v1/users/{id}/disable", httpx.Wrap(m.log, m.handler.disable))
	mux.Handle("POST /v1/users/{id}/enable", httpx.Wrap(m.log, m.handler.enable))
	mux.Handle("DELETE /v1/users/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) UseTokens(tokens Tokens) {
	m.svc.tokens = tokens
}

func (m *Module) UseRoles(roles Roles) {
	m.svc.roles = roles
}

func (m *Module) UseEvents(recorder events.Recorder) {
	m.svc.events = recorder
}

func (m *Module) UseClock(now func() time.Time) {
	if now == nil {
		return
	}
	m.svc.now = now
	m.svc.logins = newLoginLimiter(now)
}

func (m *Module) Allowed(ctx context.Context, userID string) (bool, error) {
	return m.svc.Allowed(ctx, userID)
}
