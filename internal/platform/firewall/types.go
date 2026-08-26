package firewall

import "time"

const (
	ProtocolTCP  = "tcp"
	ProtocolUDP  = "udp"
	ProtocolICMP = "icmp"
	ProtocolAny  = "any"

	MinPort = 1
	MaxPort = 65535

	MaxRules = 64
)

type Firewall struct {
	ID        string
	Name      string
	Rules     []Rule
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Rule struct {
	Protocol string
	FromPort int
	ToPort   int
	Source   string
}

type CreateParams struct {
	Name  string
	Rules []Rule
}

func Protocols() []string {
	return []string{ProtocolTCP, ProtocolUDP, ProtocolICMP, ProtocolAny}
}
