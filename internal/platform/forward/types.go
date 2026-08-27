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
	CreatedAt  time.Time
}

type CreateParams struct {
	ProjectID  string
	InstanceID string
	Protocol   string
	NodePort   int
	TargetPort int
}

type Endpoint struct {
	ProjectID string
	NodeID    string
	Address   string
}

func Protocols() []string {
	return []string{ProtocolTCP, ProtocolUDP}
}
