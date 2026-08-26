package token

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

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
	return "token"
}

func (m *Module) Migrations() []store.Migration {
	return []store.Migration{
		{
			Module: "token",
			Index:  1,
			SQL: `CREATE TABLE tokens (
				id           TEXT PRIMARY KEY,
				name         TEXT NOT NULL,
				role         TEXT NOT NULL,
				secret_hash  TEXT NOT NULL,
				created_at   TEXT NOT NULL,
				last_used_at TEXT NOT NULL
			)`,
		},
		{
			Module: "token",
			Index:  2,
			SQL:    `CREATE UNIQUE INDEX tokens_name_unique ON tokens (name)`,
		},
		{
			Module: "token",
			Index:  3,
			SQL:    `CREATE UNIQUE INDEX tokens_secret_unique ON tokens (secret_hash)`,
		},
	}
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/tokens", httpx.Wrap(m.log, m.handler.create))
	mux.Handle("GET /v1/tokens", httpx.Wrap(m.log, m.handler.list))
	mux.Handle("DELETE /v1/tokens/{id}", httpx.Wrap(m.log, m.handler.delete))
}

func (m *Module) Verify(ctx context.Context, secret string) (Identity, error) {
	return m.svc.verify(ctx, secret)
}

func (m *Module) EnsureBootstrap(ctx context.Context, dir string) error {
	secret, err := m.svc.ensureBootstrap(ctx)
	if err != nil {
		return err
	}
	if secret == "" {
		return nil
	}

	path := filepath.Join(dir, BootstrapFileName)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return fmt.Errorf("write the bootstrap token: %w", err)
	}

	m.log.Warn("no tokens existed, so an admin token was created",
		"name", BootstrapName,
		"file", path,
		"note", "read it once, then revoke it after making your own",
	)
	return nil
}
