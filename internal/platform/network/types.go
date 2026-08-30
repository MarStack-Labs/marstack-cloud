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
	CIDR6     string
	Gateway6  string
	Bridge    string
	CreatedAt time.Time
}

func (n Network) DualStack() bool {
	return n.CIDR6 != ""
}

type Slice struct {
	NetworkID string
	NodeID    string
	CIDR      string
	CIDR6     string
	CreatedAt time.Time
}

type NIC struct {
	InstanceID string
	NetworkID  string
	Device     int
	NodeID     string
	IP         string
	IP6        string
	MAC        string
	CreatedAt  time.Time
}

const MaxNICs = 4

type CreateParams struct {
	ProjectID string
	Name      string
	CIDR      string
	CIDR6     string
}
