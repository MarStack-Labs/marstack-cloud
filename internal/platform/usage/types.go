package usage

import "time"

type NodeSample struct {
	NodeID        string
	CPUPercent    float64
	MemoryUsedMiB int
	MemoryMiB     int
	ReportedAt    time.Time
}

type InstanceSample struct {
	InstanceID    string
	NodeID        string
	CPUPercent    float64
	MemoryUsedMiB int
	ReportedAt    time.Time
}

type Report struct {
	Node      NodeSample
	Instances []InstanceSample
}
