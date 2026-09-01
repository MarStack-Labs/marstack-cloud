package instance

import "time"

type Isolation string

const (
	IsolationContainer Isolation = "container"
	IsolationVM        Isolation = "vm"
	IsolationMicroVM   Isolation = "microvm"
	IsolationSandbox   Isolation = "sandbox"
)

func AllIsolations() []string {
	return []string{
		string(IsolationContainer),
		string(IsolationVM),
		string(IsolationMicroVM),
		string(IsolationSandbox),
	}
}

type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

type ObservedState string

const (
	ObservedPending ObservedState = "pending"
	ObservedRunning ObservedState = "running"
	ObservedStopped ObservedState = "stopped"
	ObservedFailed  ObservedState = "failed"
)

func AllObservedStates() []string {
	return []string{
		string(ObservedPending),
		string(ObservedRunning),
		string(ObservedStopped),
		string(ObservedFailed),
	}
}

const MaxObservedMessage = 512

type RestartPolicy string

const (
	RestartNever     RestartPolicy = "never"
	RestartOnFailure RestartPolicy = "on-failure"
	RestartAlways    RestartPolicy = "always"

	DefaultRestartPolicy = RestartAlways
)

func AllRestartPolicies() []string {
	return []string{
		string(RestartNever),
		string(RestartOnFailure),
		string(RestartAlways),
	}
}

const (
	MinVCPU      = 1
	MaxVCPU      = 256
	MinMemoryMiB = 128
	MaxMemoryMiB = 1024 * 1024

	DefaultVCPU      = 1
	DefaultMemoryMiB = 512
)

type Instance struct {
	ID              string
	ProjectID       string
	Name            string
	Isolation       Isolation
	Image           string
	ISO             string
	Kernel          string
	DiskGiB         int
	FirewallID      string
	Command         []string
	NetworkID       string
	VCPU            int
	MemoryMiB       int
	RestartPolicy   RestartPolicy
	RestartCount    int
	Desired         DesiredState
	Observed        ObservedState
	ObservedMessage string
	ExitCode        *int
	NodeID          string
	Group           string
	Strict          bool
	NodeSelector    map[string]string
	EnvSealed       string
	SealKeyID       string
	EnvNames        []string
	FilesSealed     string
	FilePaths       []string
	ExtraNetworks   []string
	SSHKeys         []string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"`
}

type Footprint struct {
	Instances int
	VCPU      int
	MemoryMiB int
}

type CreateParams struct {
	ProjectID     string
	Name          string
	Group         string
	Strict        bool
	NodeSelector  map[string]string
	Env           map[string]string
	Files         []File
	ExtraNetworks []string
	Keys          []string
	Isolation     string
	Image         string
	ISO           string
	Kernel        string
	DiskGiB       int
	FirewallID    string
	Command       []string
	NetworkID     string
	RestartPolicy string
	VCPU          int
	MemoryMiB     int
}

const (
	MaxCommandArgs = 64

	MaxNodeSelector = 8

	MaxExtraNetworks = 3

	MaxFiles       = 16
	MaxFileBytes   = 128 * 1024
	MaxFilePathLen = 4096

	MaxEnv         = 64
	MaxEnvNameLen  = 128
	MaxEnvValueLen = 4096

	DefaultDiskGiB = 10
	MaxDiskGiB     = 2048
)
