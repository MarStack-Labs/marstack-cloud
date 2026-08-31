package service

import "time"

const (
	MaxReplicas      = 32
	ReconcileEvery   = 10 * time.Second
	MaxNameLength    = 40
	SuffixLength     = 6
	MaxCreatePerPass = 4
)

const (
	MaxNodeSelector  = 8
	MaxEnv           = 64
	MaxExtraNetworks = 3
	MaxFiles         = 16
)

type Template struct {
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
	Group         string
	Strict        bool
	NodeSelector  map[string]string
	ExtraNetworks []string
	Env           map[string]string
	EnvSealed     string
	EnvKeyID      string
	EnvNames      []string
	Files         []File
	FilesSealed   string
	FilePaths     []string
	Keys          []string
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"`
}

type Service struct {
	ID        string
	ProjectID string
	Name      string
	Replicas  int
	Template  Template
	Members   []Member
	Blocked   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Member struct {
	InstanceID string
	CreatedAt  time.Time
}

type CreateParams struct {
	ProjectID string
	Name      string
	Replicas  int
	Template  Template
}

type Workload struct {
	ProjectID string
	Name      string
	Template  Template
	Env       map[string]string
	Files     []File
}
