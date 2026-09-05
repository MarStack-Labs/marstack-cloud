package volume

import "time"

const (
	MinSizeGiB = 1
	MaxSizeGiB = 4096

	StateFree      = "free"
	StateAttached  = "attached"
	StateDetaching = "detaching"

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
	CloneFrom   string
	CloneSnap   string
	Detaching   bool
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
	Detached  bool
	VolumeID  string
	Snapshots []ReportedSnapshot
	Restored  string
	Error     string
}

func (v Volume) State() string {
	switch {
	case v.Detaching:
		return StateDetaching
	case v.InstanceID != "":
		return StateAttached
	default:
		return StateFree
	}
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
	Observed   string
	InstanceID string
	ProjectID  string
	NodeID     string
	Isolation  string
	Running    bool
}
