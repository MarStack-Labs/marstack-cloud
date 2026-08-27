package backup

import "time"

const (
	StatePending = "pending"
	StateReady   = "ready"
	StateFailed  = "failed"

	MaxBytes = int64(1) << 42
)

type Backup struct {
	ID        string
	ProjectID string
	VolumeID  string
	NodeID    string
	Name      string
	State     string
	Message   string
	SizeBytes int64
	Checksum  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateParams struct {
	ProjectID string
	VolumeID  string
	NodeID    string
	Name      string
}

type Source struct {
	ProjectID string
	NodeID    string
	Name      string
}

func States() []string {
	return []string{StatePending, StateReady, StateFailed}
}
