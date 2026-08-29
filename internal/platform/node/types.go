package node

import "time"

type Status string

const (
	StatusReady       Status = "ready"
	StatusUnreachable Status = "unreachable"
)

const ReadyWindow = 30 * time.Second

type Node struct {
	ID           string
	Schedulable  bool
	Draining     bool
	Name         string
	Zone         string
	Address      string
	Arch         string
	OS           string
	CPUs         int
	MemoryMiB    int
	AgentVersion string
	Labels       map[string]string
	RegisteredAt time.Time
	LastSeenAt   time.Time
}

const (
	MaxLabels      = 32
	MaxLabelLength = 63
)

func (n Node) Matches(selector map[string]string) bool {
	for key, want := range selector {
		if n.Labels[key] != want {
			return false
		}
	}
	return true
}

func (n Node) StatusAt(now time.Time) Status {
	if now.Sub(n.LastSeenAt) <= ReadyWindow {
		return StatusReady
	}
	return StatusUnreachable
}

type RegisterParams struct {
	Name         string
	Zone         string
	Address      string
	Arch         string
	OS           string
	CPUs         int
	MemoryMiB    int
	AgentVersion string
}
