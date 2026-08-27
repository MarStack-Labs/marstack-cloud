package image

import "time"

const (
	KindDisk   = "disk"
	KindISO    = "iso"
	KindKernel = "kernel"

	ArchARM64 = "arm64"
	ArchAMD64 = "amd64"
)

type Image struct {
	ID        string
	ProjectID string
	Name      string
	Kind      string
	Arch      string
	Source    string
	Checksum  string
	SizeBytes int64
	CreatedAt time.Time
}

type Staged struct {
	ImageID   string
	SizeBytes int64
}

type Placement struct {
	Image Image
	Nodes []string
}

type CreateParams struct {
	ProjectID string
	Name      string
	Kind      string
	Arch      string
	Source    string
	Checksum  string
}

func Kinds() []string {
	return []string{KindDisk, KindISO, KindKernel}
}

func Arches() []string {
	return []string{ArchARM64, ArchAMD64}
}
