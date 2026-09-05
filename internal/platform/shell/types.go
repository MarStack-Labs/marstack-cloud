package shell

import "time"

const (
	StateWaiting  = "waiting"
	StateAttached = "attached"
	StateClosed   = "closed"

	MaxWaitingPerProject = 4
	Abandoned            = 2 * time.Minute
	SweepInterval        = 30 * time.Second
	PipeBytes            = 64 << 10
)

type Session struct {
	ID         string
	ProjectID  string
	InstanceID string
	NodeID     string
	Isolation  string
	Command    []string
	State      string
	Message    string
	CreatedAt  time.Time
	TakenAt    time.Time
	EndedAt    time.Time
}

type StartParams struct {
	ProjectID   string
	InstanceRef string
	Command     []string
}

type Target struct {
	InstanceID string
	NodeID     string
	Isolation  string
	Observed   string
}
