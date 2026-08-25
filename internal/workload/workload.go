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
	Bridge     string
	BridgeAddr string
	IP         string
	Prefix     int
	Gateway    string
	MAC        string
}

type Spec struct {
	InstanceID string
	Name       string
	Image      string
	Command    []string
	VCPU       int
	MemoryMiB  int
	Network    *NetworkConfig
}

type State struct {
	Phase   Phase
	Message string
}

type Runtime interface {
	Name() string
	Start(ctx context.Context, spec Spec) error
	Stop(ctx context.Context, instanceID string) error
	Status(ctx context.Context, instanceID string) (State, error)
	Remove(ctx context.Context, instanceID string) error
}
