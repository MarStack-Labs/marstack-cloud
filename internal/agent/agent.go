package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/version"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	DefaultInterval = 10 * time.Second

	minBackoff = 1 * time.Second
	maxBackoff = 30 * time.Second
)

type Config struct {
	Endpoint string
	Name     string
	Zone     string
	Address  string
	Interval time.Duration
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	return c
}

type Deps struct {
	Runtime  workload.Runtime
	Datapath workload.Datapath
	Resolver workload.Resolver
}

type Agent struct {
	cfg      Config
	client   *client
	log      *slog.Logger
	host     hostInfo
	runtime  workload.Runtime
	datapath workload.Datapath
	resolver workload.Resolver
	nodeID   string
}

func New(cfg Config, deps Deps, log *slog.Logger) *Agent {
	cfg = cfg.withDefaults()
	return &Agent{
		cfg:      cfg,
		client:   newClient(cfg.Endpoint),
		log:      log,
		host:     inspectHost(),
		runtime:  deps.Runtime,
		datapath: deps.Datapath,
		resolver: deps.Resolver,
	}
}

func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent starting",
		"node", a.cfg.Name,
		"endpoint", a.cfg.Endpoint,
		"arch", a.host.Arch,
		"address", a.cfg.Address,
		"cpus", a.host.CPUs,
		"memory_mib", a.host.MemoryMiB,
		"runtime", a.runtimeName(),
	)

	if err := a.registerWithRetry(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.log.Info("agent stopping", "node", a.cfg.Name)
			return nil
		case <-ticker.C:
			a.tick(ctx)
		}
	}
}

func (a *Agent) tick(ctx context.Context) {
	if a.nodeID == "" {
		if err := a.register(ctx); err != nil {
			a.log.Warn("register failed, will retry", "error", err)
		}
		return
	}

	if _, err := a.client.heartbeat(ctx, a.nodeID); err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.Code == "node_not_found" {
			a.log.Warn("control plane forgot this node, registering again", "node_id", a.nodeID)
			a.nodeID = ""
			return
		}
		a.log.Warn("heartbeat failed", "error", err)
		return
	}
	a.log.Debug("heartbeat sent", "node_id", a.nodeID)

	a.reconcile(ctx)
}

func (a *Agent) register(ctx context.Context) error {
	view, err := a.client.register(ctx, registerBody{
		Name:         a.cfg.Name,
		Zone:         a.cfg.Zone,
		Address:      a.cfg.Address,
		Arch:         a.host.Arch,
		OS:           a.host.OS,
		CPUs:         a.host.CPUs,
		MemoryMiB:    a.host.MemoryMiB,
		AgentVersion: version.Version,
	})
	if err != nil {
		return err
	}

	a.nodeID = view.ID
	a.log.Info("node registered", "node_id", view.ID, "name", view.Name, "zone", view.Zone)
	return nil
}

func (a *Agent) registerWithRetry(ctx context.Context) error {
	backoff := minBackoff
	for {
		err := a.register(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.Status < 500 {
			return err
		}

		a.log.Warn("register failed, retrying", "error", err, "in", backoff.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (a *Agent) runtimeName() string {
	if a.runtime == nil {
		return "none"
	}
	return a.runtime.Name()
}
