package volume

import "time"

const (
	MinSizeGiB = 1
	MaxSizeGiB = 4096

	StateFree     = "free"
	StateAttached = "attached"
)

type Volume struct {
	ID         string
	Name       string
	SizeGiB    int
	NodeID     string
	InstanceID string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (v Volume) State() string {
	if v.InstanceID != "" {
		return StateAttached
	}
	return StateFree
}

type CreateParams struct {
	Name    string
	SizeGiB int
}

type Placement struct {
	NodeID    string
	Isolation string
}
