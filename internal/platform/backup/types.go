package backup

import "time"

const TransferWindow = 30 * time.Minute

const (
	StatePending = "pending"
	StateReady   = "ready"
	StateFailed  = "failed"

	MaxBytes = int64(1) << 42

	MinEvery = time.Minute
	MaxEvery = 365 * 24 * time.Hour
	MinKeep  = 1
	MaxKeep  = 365

	SweepInterval = time.Minute
)

type Backup struct {
	ID              string
	ProjectID       string
	ScheduleID      string
	VolumeID        string
	NodeID          string
	Name            string
	State           string
	Message         string
	SizeBytes       int64
	Checksum        string
	KeyID           string
	VolumeKeySealed string
	VolumeKeyID     string
	ContentKey      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateParams struct {
	ProjectID  string
	VolumeID   string
	NodeID     string
	Name       string
	ScheduleID string
}

type Envelope struct {
	SizeBytes int64
	KeySealed string
	KeyID     string
}

type Schedule struct {
	ID        string
	ProjectID string
	VolumeID  string
	Every     time.Duration
	Keep      int
	NextAt    time.Time
	LastAt    time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ScheduleParams struct {
	ProjectID string
	VolumeID  string
	Every     string
	Keep      int
}

type Source struct {
	ID        string
	ProjectID string
	NodeID    string
	Name      string
	Encrypted bool
	KeySealed string
	KeyID     string
}

func States() []string {
	return []string{StatePending, StateReady, StateFailed}
}
