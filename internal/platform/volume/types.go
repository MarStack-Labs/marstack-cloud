package volume

import "time"

const (
	MinSizeGiB = 1
	MaxSizeGiB = 4096

	StateFree     = "free"
	StateAttached = "attached"

	SnapshotPending = "pending"
	SnapshotReady   = "ready"
	SnapshotFailed  = "failed"
)

type Volume struct {
	ID          string
	ProjectID   string
	Name        string
	SizeGiB     int
	NodeID      string
	InstanceID  string
	RestoreFrom string
	BackupID    string
	Encrypted   bool
	KeySealed   string
	KeyID       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Snapshot struct {
	ID         string
	VolumeID   string
	Name       string
	State      string
	Message    string
	SizeBytes  int64
	ScheduleID string
	CreatedAt  time.Time
}

type ReportedSnapshot struct {
	Name      string
	SizeBytes int64
}

type NodeReport struct {
	VolumeID  string
	Snapshots []ReportedSnapshot
	Restored  string
	Error     string
}

func (v Volume) State() string {
	if v.InstanceID != "" {
		return StateAttached
	}
	return StateFree
}

type Footprint struct {
	Volumes int
	SizeGiB int
}

type CreateParams struct {
	ProjectID string
	Name      string
	SizeGiB   int
	BackupID  string
	Encrypted bool
}

type Placement struct {
	ProjectID string
	NodeID    string
	Isolation string
	Running   bool
}
