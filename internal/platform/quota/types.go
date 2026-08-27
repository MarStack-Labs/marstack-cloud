package quota

import "time"

const Unlimited = 0

type Limits struct {
	Instances int
	VCPU      int
	MemoryMiB int
	Volumes   int
	VolumeGiB int
}

type Consumed struct {
	Instances int
	VCPU      int
	MemoryMiB int
	Volumes   int
	VolumeGiB int
}

type Claim struct {
	Instances int
	VCPU      int
	MemoryMiB int
	Volumes   int
	VolumeGiB int
}

type Quota struct {
	ProjectID string
	Limits    Limits
	UpdatedAt time.Time
}

type Report struct {
	ProjectID string
	Limits    Limits
	Consumed  Consumed
	UpdatedAt time.Time
}

func (l Limits) Empty() bool {
	return l == Limits{}
}

type dimension struct {
	name     string
	unit     string
	limit    int
	consumed int
	claim    int
}

func (l Limits) against(consumed Consumed, claim Claim) []dimension {
	return []dimension{
		{"instances", "", l.Instances, consumed.Instances, claim.Instances},
		{"vcpu", "", l.VCPU, consumed.VCPU, claim.VCPU},
		{"memory", "MiB", l.MemoryMiB, consumed.MemoryMiB, claim.MemoryMiB},
		{"volumes", "", l.Volumes, consumed.Volumes, claim.Volumes},
		{"volume capacity", "GiB", l.VolumeGiB, consumed.VolumeGiB, claim.VolumeGiB},
	}
}
