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
	Name         string
	Zone         string
	Arch         string
	OS           string
	CPUs         int
	MemoryMiB    int
	AgentVersion string
	RegisteredAt time.Time
	LastSeenAt   time.Time
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
	Arch         string
	OS           string
	CPUs         int
	MemoryMiB    int
	AgentVersion string
}
