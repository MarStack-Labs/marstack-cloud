package app

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/certs"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ratelimit"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/s3"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/platform/acme"
	"github.com/marstack-labs/marstack-cloud/internal/platform/audit"
	"github.com/marstack-labs/marstack-cloud/internal/platform/autoscale"
	"github.com/marstack-labs/marstack-cloud/internal/platform/backup"
	"github.com/marstack-labs/marstack-cloud/internal/platform/balancer"
	"github.com/marstack-labs/marstack-cloud/internal/platform/dns"
	"github.com/marstack-labs/marstack-cloud/internal/platform/event"
	"github.com/marstack-labs/marstack-cloud/internal/platform/exec"
	"github.com/marstack-labs/marstack-cloud/internal/platform/firewall"
	"github.com/marstack-labs/marstack-cloud/internal/platform/forward"
	"github.com/marstack-labs/marstack-cloud/internal/platform/image"
	"github.com/marstack-labs/marstack-cloud/internal/platform/instance"
	"github.com/marstack-labs/marstack-cloud/internal/platform/job"
	"github.com/marstack-labs/marstack-cloud/internal/platform/keypair"
	"github.com/marstack-labs/marstack-cloud/internal/platform/logs"
	"github.com/marstack-labs/marstack-cloud/internal/platform/network"
	"github.com/marstack-labs/marstack-cloud/internal/platform/node"
	"github.com/marstack-labs/marstack-cloud/internal/platform/project"
	"github.com/marstack-labs/marstack-cloud/internal/platform/quota"
	"github.com/marstack-labs/marstack-cloud/internal/platform/registry"
	"github.com/marstack-labs/marstack-cloud/internal/platform/scheduler"
	"github.com/marstack-labs/marstack-cloud/internal/platform/service"
	"github.com/marstack-labs/marstack-cloud/internal/platform/system"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
	"github.com/marstack-labs/marstack-cloud/internal/platform/usage"
	"github.com/marstack-labs/marstack-cloud/internal/platform/user"
	"github.com/marstack-labs/marstack-cloud/internal/platform/volume"
	"github.com/marstack-labs/marstack-cloud/internal/platform/webhook"
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
	Now               func() time.Time
	TLSCert           string
	TLSKey            string
	BackupKeys        []sealed.Key
	ObjectStore       s3.Config
	RatePerSecond     *int
	RateBurst         int
	ProjectPerSecond  *int
	ProjectBurst      int
	ACMEDirectory     string
	ACMEContact       string
}

func (c Config) projectPerSecond() int {
	if c.ProjectPerSecond == nil {
		return ratelimit.DefaultProjectPerSecond
	}
	return *c.ProjectPerSecond
}

func (c Config) projectBurst() int {
	if c.ProjectBurst <= 0 {
		return ratelimit.DefaultProjectBurst
	}
	return c.ProjectBurst
}

func (c Config) ratePerSecond() int {
	if c.RatePerSecond == nil {
		return ratelimit.DefaultPerSecond
	}
	return *c.RatePerSecond
}

func (c Config) rateBurst() int {
	if c.RateBurst <= 0 {
		return ratelimit.DefaultBurst
	}
	return c.RateBurst
}

func (c Config) servesTLS() bool {
	return c.TLSCert != "" && c.TLSKey != ""
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
	cfg          Config
	log          *slog.Logger
	store        *store.Store
	modules      []Module
	networks     *network.Module
	projects     *project.Module
	backups      *backup.Module
	volumes      *volume.Module
	instances    *instance.Module
	jobs         *job.Module
	commands     *exec.Module
	scalers      *autoscale.Module
	services     *service.Module
	webhooks     *webhook.Module
	balancers    *balancer.Module
	certificates *acme.Module
	forwards     *forward.Module
	tokens       *token.Module
	trail        *audit.Module
	scheduler    *scheduler.Scheduler
	limiter      *ratelimit.Limiter
	tenants      *ratelimit.Limiter
	router       http.Handler
	http         *http.Server
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

	backups, err := openBackups(ctx, cfg, st, log)
	if err != nil {
		st.Close()
		return nil, err
	}
	forwards := forward.New(st, forwardAddresses{networks: networks, instances: instances}, log)
	balancers := balancer.New(st, log)
	registries := registry.New(st, log)
	certificates := acme.New(st, log)
	firewalls := firewall.New(st, log)
	keys := keypair.New(st, log)
	events := event.New(st, log)
	services := service.New(st, log)
	webhooks := webhook.New(st, log)
	projects := project.New(st, log)
	quotas := quota.New(st, log)
	tokens := token.New(st, log, cfg.Now)

	backups.UseVolumes(backupVolumes{volumes: volumes})
	backups.UseEvents(events)
	volumes.UseBackups(volumeBackups{backups: backups})
	if cfg.Now != nil {
		volumes.UseClock(cfg.Now)
	}
	volumes.UseKeys(sealed.NewKeyring(cfg.BackupKeys))
	instances.UseSealing(sealed.NewKeyring(cfg.BackupKeys))
	balancers.UseSealing(sealed.NewKeyring(cfg.BackupKeys))
	services.UseSealing(sealed.NewKeyring(cfg.BackupKeys))
	forwards.UseSealing(sealed.NewKeyring(cfg.BackupKeys))
	registries.UseKeys(sealed.NewKeyring(cfg.BackupKeys))
	if cfg.Now != nil {
		registries.UseClock(cfg.Now)
	}
	tokens.UseProjects(projects)
	quotas.UseProjects(projects)
	quotas.UseUsage(projectUsage{instances: instances, volumes: volumes})
	instances.UseQuota(instanceQuota{quotas: quotas})
	instances.UseKeys(keys)
	forwards.UseEvents(events)
	balancers.UseEvents(events)
	certificates.UseBalancers(balancers)
	certificates.UseEvents(events)
	if cfg.ACMEDirectory != "" {
		certificates.UseDirectory(cfg.ACMEDirectory, cfg.ACMEContact)
	}
	if cfg.Now != nil {
		certificates.UseClock(cfg.Now)
	}
	balancers.UseMembers(balancerMembers{networks: networks, instances: instances})
	balancers.UsePorts(forwards)
	forwards.UseBalancers(balancers)
	volumes.UseQuota(volumeQuota{quotas: quotas})
	projects.UseOccupancy(projectOccupancy{
		tokens:    tokens,
		instances: instances,
		volumes:   volumes,
	})
	usages := usage.New(st, log)
	usages.UseWorkloads(usageWorkloads{instances: instances})
	people := user.New(st, log)
	people.UseTokens(tokens)
	people.UseRoles(tokens)
	people.UseClock(cfg.Now)
	tokens.UseUsers(people)
	scalers := autoscale.New(st, log)
	scalers.UseServices(scalableServices{services: services})
	scalers.UseLoad(instanceLoad{usage: usages, instances: instances})
	scalers.UseClock(cfg.Now)
	a.scalers = scalers
	commands := exec.New(st, log)
	commands.UseInstances(execInstances{instances: instances})
	commands.UseClock(cfg.Now)
	a.commands = commands
	jobs := job.New(st, log)
	jobs.UseWorkloads(jobWorkloads{instances: instances})
	jobs.UseNetworks(networks)
	jobs.UseFirewalls(firewalls)
	jobs.UseSealing(sealed.NewKeyring(cfg.BackupKeys))
	jobs.UseClock(cfg.Now)
	a.jobs = jobs
	logbook := logs.New(st, log)
	logbook.UseInstances(logInstances{instances: instances})
	logbook.UseClock(cfg.Now)
	trail := audit.New(st, log)
	a.trail = trail
	a.tokens = tokens
	a.projects = projects
	a.backups = backups
	a.volumes = volumes
	a.instances = instances
	a.services = services
	a.webhooks = webhooks
	a.balancers = balancers
	a.certificates = certificates
	a.forwards = forwards

	a.modules = []Module{
		system.New(st, log),
		nodes,
		networks,
		instances,
		dns.New(log, dnsInstances{instances: instances}, networks),
		image.New(st, log),
		registries,
		certificates,
		volumes,
		backups,
		forwards,
		balancers,
		firewalls,
		usages,
		scalers,
		commands,
		jobs,
		logbook,
		trail,
		projects,
		quotas,
		events,
		services,
		webhooks,
		keys,
		tokens,
		people,
	}
	instances.UseVolumes(volumes)
	instances.UseForwards(forwards)
	instances.UseBalancers(balancers)
	instances.UseLogs(logbook)
	instances.UseExecs(commands)
	instances.UseDataDir(cfg.DataDir)
	jobs.UseEvents(events)
	scalers.UseEvents(events)
	people.UseEvents(events)
	instances.UseEvents(events)
	services.UseWorkloads(serviceWorkloads{instances: instances})
	services.UseNetworks(networks)
	services.UseFirewalls(firewalls)
	services.UseEvents(events)
	balancers.UseServices(services)
	webhooks.UseEvents(webhookEvents{events: events})
	instances.UseFirewalls(firewalls)
	a.scheduler = scheduler.New(
		nodeSource{nodes: nodes},
		instanceSource{instances: instances},
		addressSource{networks: networks},
		log,
		cfg.SchedulerInterval,
	)

	a.scheduler.UseLoad(nodeLoad{usage: usages, now: time.Now})
	a.scheduler.UseEvents(events)

	if err := a.migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}

	if _, err := a.projects.EnsureDefault(ctx); err != nil {
		st.Close()
		return nil, err
	}

	if _, err := a.networks.EnsureDefault(ctx, project.DefaultID); err != nil {
		st.Close()
		return nil, err
	}

	if err := a.tokens.EnsureBootstrap(ctx, cfg.DataDir); err != nil {
		st.Close()
		return nil, err
	}

	a.limiter = ratelimit.New(cfg.ratePerSecond(), cfg.rateBurst(), cfg.Now)
	a.tenants = ratelimit.New(cfg.projectPerSecond(), cfg.projectBurst(), cfg.Now)
	a.router = a.buildRouter()

	var tlsConfig *tls.Config
	if cfg.servesTLS() {
		if tlsConfig, err = certs.Server(cfg.TLSCert, cfg.TLSKey); err != nil {
			st.Close()
			return nil, err
		}
	}

	a.http = &http.Server{
		TLSConfig:         tlsConfig,
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
	if a.backups != nil {
		a.backups.Close()
	}
	return a.store.Close()
}

func openBackups(
	ctx context.Context, cfg Config, st *store.Store, log *slog.Logger,
) (*backup.Module, error) {
	keys := sealed.NewKeyring(cfg.BackupKeys)
	if cfg.ObjectStore.Endpoint == "" {
		return backup.New(st, cfg.DataDir, keys, log)
	}
	return backup.NewOnObjectStore(ctx, st, cfg.DataDir, cfg.ObjectStore, keys, log)
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
		httpx.RateLimit(a.limiter, callerKey, openToEveryone),
		httpx.Timeout(a.cfg.RequestTimeout, allowList(streamingPaths)),
		auditTrail(a.trail),
		authenticate(a.tokens, a.log),
		httpx.RateLimit(a.tenants, projectKey, openToEveryone),
	)
}

func (a *App) announce() {
	scheme := "http"
	if a.cfg.servesTLS() {
		scheme = "https"
	}
	a.log.Info("control plane listening",
		"addr", a.cfg.Listen, "scheme", scheme, "modules", len(a.modules))

	if !a.cfg.servesTLS() {
		a.log.Warn("serving plain HTTP, so every bearer token crosses the network in the clear",
			"fix", "pass --tls-cert and --tls-key")
	}

	if len(a.cfg.BackupKeys) == 0 {
		a.log.Warn("backups are stored unencrypted, so a copy of every volume sits in the "+
			"data directory in the clear", "fix", "pass --backup-key-file")
		return
	}
	a.log.Info("backups are sealed at rest",
		"key", a.cfg.BackupKeys[0].ID(), "keys_held", len(a.cfg.BackupKeys))
}

func (a *App) listen() error {
	if a.cfg.servesTLS() {
		return a.http.ListenAndServeTLS("", "")
	}
	return a.http.ListenAndServe()
}

func (a *App) Handler() http.Handler {
	return a.router
}

func (a *App) Run(ctx context.Context) error {
	go a.scheduler.Run(ctx)
	go a.backups.Run(ctx)
	go a.volumes.Run(ctx)
	go a.jobs.Run(ctx)
	go a.commands.Run(ctx)
	go a.scalers.Run(ctx)
	go a.services.Run(ctx)
	go a.webhooks.Run(ctx)
	go a.balancers.Run(ctx)
	go a.forwards.Run(ctx)
	go a.certificates.Run(ctx)

	errc := make(chan error, 1)
	go func() {
		a.announce()
		err := a.listen()
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
