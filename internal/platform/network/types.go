package network

import "time"

const (
	DefaultName    = "default"
	DefaultCIDR    = "10.20.0.0/16"
	SliceBits      = 26
	bridgePrefix   = "msbr-"
	reservedSlices = 1
)

type Network struct {
	ID        string
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
	NodeID     string
	IP         string
	MAC        string
	CreatedAt  time.Time
}

type CreateParams struct {
	Name string
	CIDR string
}
