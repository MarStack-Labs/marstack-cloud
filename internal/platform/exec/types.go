package exec

import "time"

const (
	MaxCommandParts = 32
	MaxPartLength   = 1024
	MaxOutputBytes  = 256 << 10

	MinTimeout     = time.Second
	MaxTimeout     = 5 * time.Minute
	DefaultTimeout = 30 * time.Second

	MaxWaitingPerProject = 16
	KeepPerInstance      = 20
	Abandoned            = 15 * time.Minute
	SweepInterval        = time.Minute
)

const (
	StateWaiting = "waiting"
	StateRunning = "running"
	StateDone    = "done"
	StateLost    = "lost"
)

type Run struct {
	ID         string
	ProjectID  string
	InstanceID string
	NodeID     string
	Isolation  string
	Command    []string
	Timeout    time.Duration
	State      string
	Output     string
	Truncated  bool
	ExitCode   *int
	Message    string
	CreatedAt  time.Time
	TakenAt    time.Time
	EndedAt    time.Time
}

type StartParams struct {
	ProjectID   string
	InstanceRef string
	Command     []string
	Timeout     string
}

type Result struct {
	Output    string
	Truncated bool
	ExitCode  *int
	Message   string
}
