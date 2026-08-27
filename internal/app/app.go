package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/platform/audit"
	"github.com/marstack-labs/marstack-cloud/internal/platform/dns"
	"github.com/marstack-labs/marstack-cloud/internal/platform/firewall"
	"github.com/marstack-labs/marstack-cloud/internal/platform/forward"
	"github.com/marstack-labs/marstack-cloud/internal/platform/image"
	"github.com/marstack-labs/marstack-cloud/internal/platform/instance"
	"github.com/marstack-labs/marstack-cloud/internal/platform/network"
	"github.com/marstack-labs/marstack-cloud/internal/platform/node"
	"github.com/marstack-labs/marstack-cloud/internal/platform/project"
	"github.com/marstack-labs/marstack-cloud/internal/platform/scheduler"
	"github.com/marstack-labs/marstack-cloud/internal/platform/system"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
	"github.com/marstack-labs/marstack-cloud/internal/platform/usage"
	"github.com/marstack-labs/marstack-cloud/internal/platform/volume"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type Module interface {
	Name() string
	Migrations() []store.Migration
	Routes(mux *http.ServeMux)
}

type Config struct {
	Listen            string
	DataDir           string
	RequestTimeout    time.Duration
	SchedulerInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:7443"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 30 * time.Second
	}
	if c.SchedulerInterval <= 0 {
		c.SchedulerInterval = scheduler.DefaultInterval
	}
	return c
}

type App struct {
	cfg       Config
	log       *slog.Logger
	store     *store.Store
	modules   []Module
	networks  *network.Module
	projects  *project.Module
	tokens    *token.Module
	trail     *audit.Module
	scheduler *scheduler.Scheduler
	router    http.Handler
	http      *http.Server
}

func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	cfg = cfg.withDefaults()

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}

	nodes := node.New(st, log)
	networks := network.New(st, log)
	instances := instance.New(st, log, networks)

	a := &App{cfg: cfg, log: log, store: st, networks: networks}
	volumes := volume.New(st, volumeInstances{instances: instances}, log)
	forwards := forward.New(st, forwardAddresses{networks: networks}, log)
	firewalls := firewall.New(st, log)
	projects := project.New(st, log)
	tokens := token.New(st, log)
	tokens.UseProjects(projects)
	projects.UseOccupancy(projectOccupancy{tokens: tokens})
	usages := usage.New(st, log)
	trail := audit.New(st, log)
	a.trail = trail
	a.tokens = tokens
	a.projects = projects

	a.modules = []Module{
		system.New(st, log),
		nodes,
		networks,
		instances,
		dns.New(log, dnsInstances{instances: instances}, networks),
		image.New(st, log),
		volumes,
		forwards,
		firewalls,
		usages,
		trail,
		projects,
		tokens,
	}
	instances.UseVolumes(volumes)
	instances.UseForwards(forwards)
	instances.UseFirewalls(firewalls)
	a.scheduler = scheduler.New(
		nodeSource{nodes: nodes},
		instanceSource{instances: instances},
		addressSource{networks: networks},
		log,
		cfg.SchedulerInterval,
	)

	a.scheduler.UseLoad(nodeLoad{usage: usages, now: time.Now})

	if err := a.migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}

	if _, err := a.projects.EnsureDefault(ctx); err != nil {
		st.Close()
		return nil, err
	}

	if _, err := a.networks.EnsureDefault(ctx); err != nil {
		st.Close()
		return nil, err
	}

	if err := a.tokens.EnsureBootstrap(ctx, cfg.DataDir); err != nil {
		st.Close()
		return nil, err
	}

	a.router = a.buildRouter()
	a.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           a.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	return a, nil
}

func (a *App) Close() error {
	return a.store.Close()
}

func (a *App) migrate(ctx context.Context) error {
	var all []store.Migration
	for _, m := range a.modules {
		all = append(all, m.Migrations()...)
	}
	if err := a.store.Migrate(ctx, all); err != nil {
		return err
	}
	a.log.Info("store ready", "dir", a.cfg.DataDir, "migrations", len(all))
	return nil
}

func (a *App) buildRouter() http.Handler {
	mux := http.NewServeMux()
	for _, m := range a.modules {
		m.Routes(mux)
	}
	return httpx.Chain(mux,
		httpx.RequestID(),
		httpx.Recover(a.log),
		httpx.AccessLog(a.log),
		httpx.SecureHeaders(),
		httpx.Timeout(a.cfg.RequestTimeout),
		auditTrail(a.trail),
		authenticate(a.tokens, a.log),
	)
}

func (a *App) Handler() http.Handler {
	return a.router
}

func (a *App) Run(ctx context.Context) error {
	go a.scheduler.Run(ctx)

	errc := make(chan error, 1)
	go func() {
		a.log.Info("control plane listening", "addr", a.cfg.Listen, "modules", len(a.modules))
		err := a.http.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		a.log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return a.http.Shutdown(shutdownCtx)
	}
}
