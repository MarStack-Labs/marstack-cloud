package service

import "time"

const (
	MaxReplicas      = 32
	ReconcileEvery   = 10 * time.Second
	MaxNameLength    = 40
	SuffixLength     = 6
	MaxCreatePerPass = 4
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
	Keys          []string
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
}
