package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/runtime/catalog"
	"github.com/marstack-labs/marstack-cloud/internal/version"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	DefaultInterval          = 10 * time.Second
	DefaultHeartbeatInterval = 5 * time.Second

	minBackoff = 1 * time.Second
	maxBackoff = 30 * time.Second
)

type Config struct {
	Endpoint          string
	Token             string
	FenceAfter        time.Duration
	Name              string
	Zone              string
	Address           string
	StateDir          string
	TLS               *tls.Config
	Interval          time.Duration
	HeartbeatInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if c.FenceAfter <= 0 {
		c.FenceAfter = DefaultFenceAfter
	}
	return c
}

type Deps struct {
	Runtimes map[string]workload.Runtime
	Datapath workload.Datapath
	Resolver workload.Resolver
	Catalog  *catalog.Catalog
}

type Agent struct {
	cfg      Config
	client   *client
	log      *slog.Logger
	host     hostInfo
	runtimes map[string]workload.Runtime
	datapath workload.Datapath
	resolver workload.Resolver
	catalog  *catalog.Catalog
	now      func() time.Time

	nodeMu sync.RWMutex
	nodeID string

	restartsMu sync.Mutex
	restarts   map[string]*restartState

	marksMu sync.Mutex
	marks   map[string]sampleMark

	contactMu   sync.Mutex
	lastContact time.Time

	fencedMu  sync.Mutex
	fencedFor map[string]time.Duration
}

func New(cfg Config, deps Deps, log *slog.Logger) *Agent {
	cfg = cfg.withDefaults()
	return &Agent{
		cfg:       cfg,
		client:    newClient(cfg.Endpoint, cfg.Token, cfg.TLS),
		log:       log,
		host:      inspectHost(),
		runtimes:  deps.Runtimes,
		datapath:  deps.Datapath,
		resolver:  deps.Resolver,
		catalog:   deps.Catalog,
		now:       time.Now,
		restarts:  map[string]*restartState{},
		marks:     map[string]sampleMark{},
		fencedFor: map[string]time.Duration{},
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
		"runtimes", a.runtimeNames(),
	)

	if err := a.register(ctx); err != nil {
		a.log.Warn("the control plane is unreachable at startup", "error", err)
		a.reconcileFromCache(ctx)

		if err := a.registerWithRetry(ctx); err != nil {
			return err
		}
	}

	go a.heartbeatLoop(ctx)

	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.log.Info("agent stopping", "node", a.cfg.Name)
			return nil
		case <-ticker.C:
			a.reconcileTick(ctx)
		}
	}
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.beat(ctx)
		}
	}
}

func (a *Agent) beat(ctx context.Context) {
	nodeID := a.currentNodeID()
	if nodeID == "" {
		if err := a.register(ctx); err != nil {
			a.log.Warn("register failed, will retry", "error", err)
		}
		return
	}

	if _, err := a.client.heartbeat(ctx, nodeID); err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.Code == "node_not_found" {
			a.log.Warn("control plane forgot this node, registering again", "node_id", nodeID)
			a.clearNodeID()
			return
		}
		a.log.Warn("heartbeat failed", "error", err)
		return
	}
	a.noteContact()
	a.log.Debug("heartbeat sent", "node_id", nodeID)
}

func (a *Agent) reconcileTick(ctx context.Context) {
	if a.currentNodeID() == "" {
		return
	}
	a.reconcile(ctx)
}

func (a *Agent) tick(ctx context.Context) {
	a.beat(ctx)
	a.reconcileTick(ctx)
}

func (a *Agent) currentNodeID() string {
	a.nodeMu.RLock()
	defer a.nodeMu.RUnlock()
	return a.nodeID
}

func (a *Agent) setNodeID(id string) {
	a.nodeMu.Lock()
	defer a.nodeMu.Unlock()
	a.nodeID = id
}

func (a *Agent) clearNodeID() {
	a.setNodeID("")
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

	a.noteContact()
	a.setNodeID(view.ID)
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

func (a *Agent) runtimeNames() []string {
	names := make([]string, 0, len(a.runtimes))
	for isolation := range a.runtimes {
		names = append(names, isolation)
	}
	sort.Strings(names)
	return names
}

func (a *Agent) runtimeFor(isolation string) (workload.Runtime, bool) {
	runtime, known := a.runtimes[isolation]
	return runtime, known
}
