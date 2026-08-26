package workload

import "context"

type Phase string

const (
	PhaseAbsent  Phase = "absent"
	PhaseRunning Phase = "running"
	PhaseExited  Phase = "exited"
	PhaseFailed  Phase = "failed"
)

type NetworkConfig struct {
	Bridge       string
	BridgeAddr   string
	IP           string
	Prefix       int
	Gateway      string
	MAC          string
	Nameserver   string
	SearchDomain string
}

type Disk struct {
	ID      string
	Name    string
	SizeGiB int
}

type Spec struct {
	InstanceID string
	Name       string
	Isolation  string
	Image      string
	ISO        string
	Kernel     string
	DiskGiB    int
	Volumes    []Disk
	Command    []string
	VCPU       int
	MemoryMiB  int
	Network    *NetworkConfig
}

type State struct {
	Phase    Phase
	Message  string
	ExitCode int
}

type Route struct {
	Slice string
	Via   string
}

type Keep struct {
	Bridges   []string
	Instances []string
}

type Filter struct {
	InstanceID string
	Isolation  string
	Bridge     string
	IP         string
	MAC        string
}

type Datapath interface {
	ApplyRoutes(ctx context.Context, routes []Route) error
	ApplyFilters(ctx context.Context, filters []Filter) error
	Prune(ctx context.Context, keep Keep) error
}

type Resolver interface {
	Listen(ctx context.Context, address string) error
	Update(records map[string]string)
}

type VolumeKeeper interface {
	PruneVolumes(keep []string) error
}

type Runtime interface {
	Name() string
	List(ctx context.Context) ([]string, error)
	Start(ctx context.Context, spec Spec) error
	Stop(ctx context.Context, instanceID string) error
	Status(ctx context.Context, instanceID string) (State, error)
	Remove(ctx context.Context, instanceID string) error
}
