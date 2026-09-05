package alert

import "time"

const (
	MetricCPU    = "cpu"
	MetricMemory = "memory"

	Above = "above"
	Below = "below"

	StateQuiet   = "quiet"
	StateWarming = "warming"
	StateFiring  = "firing"

	MaxPerProject = 32
	MinFor        = 30 * time.Second
	MaxFor        = 6 * time.Hour
	DefaultFor    = 2 * time.Minute
	Stale         = 3 * time.Minute
	SweepInterval = 20 * time.Second
)

func Metrics() []string     { return []string{MetricCPU, MetricMemory} }
func Comparisons() []string { return []string{Above, Below} }

type Alert struct {
	ID         string
	ProjectID  string
	Name       string
	InstanceID string
	Metric     string
	Comparison string
	Threshold  float64
	For        time.Duration
	State      string
	Since      time.Time
	LastValue  float64
	Message    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CreateParams struct {
	ProjectID  string
	Name       string
	InstanceID string
	Metric     string
	Comparison string
	Threshold  float64
	For        string
}

type Sample struct {
	CPUPercent    float64
	MemoryPercent float64
	MemoryKnown   bool
	ReportedAt    time.Time
}
