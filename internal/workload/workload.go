package workload

import (
	"context"
	"io"
)

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
	KeyFile string
}

type SnapshotPlan struct {
	VolumeID  string
	Wanted    []string
	RestoreTo string
	KeyFile   string
}

type SnapshotState struct {
	VolumeID string
	Present  []SnapshotFile
	Restored string
	Error    string
}

type SnapshotFile struct {
	Name  string
	Bytes int64
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

type Publish struct {
	Protocol   string
	NodePort   int
	TargetPort int
	Address    string
}

type GuardRule struct {
	Protocol string
	FromPort int
	ToPort   int
	Source   string
}

type Guard struct {
	InstanceID string
	Isolation  string
	IP         string
	Rules      []GuardRule
}

type Datapath interface {
	ApplyRoutes(ctx context.Context, routes []Route) error
	ApplyForwards(ctx context.Context, forwards []Publish) error
	ApplyGuards(ctx context.Context, guards []Guard) error
	ApplyFilters(ctx context.Context, filters []Filter) error
	Prune(ctx context.Context, keep Keep) error
}

type Resolver interface {
	Listen(ctx context.Context, address string) error
	Update(records map[string]string)
}

type Sample struct {
	CPUSeconds float64
	MemoryMiB  int
}

type Sampler interface {
	Sample(instanceID string) (Sample, bool)
}

type VolumeKeeper interface {
	PruneVolumes(keep []string) error
	SyncSnapshots(plans []SnapshotPlan) []SnapshotState
}

type VolumeArchiver interface {
	HasVolume(volumeID string) bool
	ExportVolume(volumeID, keyFile string) (io.ReadCloser, error)
	ImportVolume(volumeID string, content io.Reader) error
}

type Runtime interface {
	Name() string
	List(ctx context.Context) ([]string, error)
	Start(ctx context.Context, spec Spec) error
	Stop(ctx context.Context, instanceID string) error
	Status(ctx context.Context, instanceID string) (State, error)
	Remove(ctx context.Context, instanceID string) error
}
