package forward

import "time"

const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"

	MinPort = 1
	MaxPort = 65535
)

type Forward struct {
	ID         string
	ProjectID  string
	InstanceID string
	Protocol   string
	NodePort   int
	TargetPort int
	NodeID     string
	Address    string
	Family     string
	CreatedAt  time.Time
}

type CreateParams struct {
	ProjectID  string
	InstanceID string
	Protocol   string
	NodePort   int
	TargetPort int
	Family     string
}

type Endpoint struct {
	ProjectID string
	NodeID    string
	Address   string
	Address6  string
}

const (
	FamilyIPv4 = "ipv4"
	FamilyIPv6 = "ipv6"
)

func Families() []string {
	return []string{FamilyIPv4, FamilyIPv6}
}

func Protocols() []string {
	return []string{ProtocolTCP, ProtocolUDP}
}
