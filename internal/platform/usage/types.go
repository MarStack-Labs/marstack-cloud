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

const (
	BucketSize     = time.Minute
	HistoryBuckets = 1440
	PruneEvery     = 200
	MaxWindow      = 24 * time.Hour
	DefaultWindow  = time.Hour
)

type Bucket struct {
	At            time.Time
	Samples       int
	CPUAverage    float64
	CPUPeak       float64
	MemoryAverage int
	MemoryPeak    int
	MemoryMiB     int
}

type History struct {
	Subject string
	Buckets []Bucket
}
