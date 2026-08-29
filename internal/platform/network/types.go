package network

import "time"

const (
	DefaultName    = "default"
	DefaultCIDR    = "10.20.0.0/16"
	SupernetCIDR   = "10.0.0.0/8"
	ProjectBits    = 16
	SliceBits      = 26
	SliceBitsV6    = 64
	bridgePrefix   = "msbr-"
	reservedSlices = 1
)

type Network struct {
	ID        string
	ProjectID string
	Name      string
	CIDR      string
	Gateway   string
	Bridge    string
	CreatedAt time.Time
}

type Slice struct {
	NetworkID string
	NodeID    string
	CIDR      string
	CreatedAt time.Time
}

type NIC struct {
	InstanceID string
	NetworkID  string
	Device     int
	NodeID     string
	IP         string
	MAC        string
	CreatedAt  time.Time
}

const MaxNICs = 4

type CreateParams struct {
	ProjectID string
	Name      string
	CIDR      string
}
