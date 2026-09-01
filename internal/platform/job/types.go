package job

import "time"

const (
	MaxNameLength = 40
	MaxEnv        = 64
	MaxFiles      = 16
	MaxNodeSelect = 8

	MinEvery = time.Minute
	MaxEvery = 365 * 24 * time.Hour

	MinRetries = 0
	MaxRetries = 10
	MinKeep    = 1
	MaxKeep    = 100

	SweepInterval  = 15 * time.Second
	MaxFirePerPass = 4
)

const (
	RunPending   = "pending"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
)

type Template struct {
	Isolation    string            `json:"isolation"`
	Image        string            `json:"image,omitempty"`
	Kernel       string            `json:"kernel,omitempty"`
	Command      []string          `json:"command,omitempty"`
	NetworkID    string            `json:"network_id,omitempty"`
	FirewallID   string            `json:"firewall_id,omitempty"`
	VCPU         int               `json:"vcpu,omitempty"`
	MemoryMiB    int               `json:"memory_mib,omitempty"`
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	EnvNames     []string          `json:"env_names,omitempty"`
	FilePaths    []string          `json:"file_paths,omitempty"`
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"`
}

type Job struct {
	ID          string
	ProjectID   string
	Name        string
	Template    Template
	EnvSealed   string
	FilesSealed string
	SealKeyID   string
	Every       time.Duration
	Retries     int
	Keep        int
	Paused      bool
	NextAt      time.Time
	LastAt      time.Time
	Runs        []Run
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Run struct {
	ID         string
	JobID      string
	InstanceID string
	Attempt    int
	State      string
	Message    string
	ExitCode   *int
	StartedAt  time.Time
	EndedAt    time.Time
}

type CreateParams struct {
	ProjectID string
	Name      string
	Template  Template
	Env       map[string]string
	Files     []File
	Every     string
	Retries   int
	Keep      int
}

type Workload struct {
	ProjectID string
	Name      string
	Template  Template
	Env       map[string]string
	Files     []File
}
