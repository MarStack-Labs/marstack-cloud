package autoscale

import "time"

const (
	MinReplicas = 1
	MaxReplicas = 32

	MinTarget = 1
	MaxTarget = 100

	DeadbandRatio = 0.15
	MaxStep       = 4

	Warmup    = 90 * time.Second
	Cooldown  = 3 * time.Minute
	StaleLoad = 60 * time.Second

	SweepInterval = 30 * time.Second
)

type Policy struct {
	ID           string
	ProjectID    string
	ServiceID    string
	Min          int
	Max          int
	TargetCPU    int
	TargetMemory int
	LastAt       time.Time
	LastReason   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type PolicyParams struct {
	ProjectID    string
	ServiceID    string
	Min          int
	Max          int
	TargetCPU    int
	TargetMemory int
}

type Member struct {
	InstanceID string
	CreatedAt  time.Time
}

type Group struct {
	ServiceID string
	ProjectID string
	Name      string
	Replicas  int
	Settled   bool
	Members   []Member
}

type Sample struct {
	CPUPercent    float64
	MemoryPercent float64
	MemoryKnown   bool
	ReportedAt    time.Time
}
