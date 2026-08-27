package balancer

import "time"

const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"

	AlgorithmRoundRobin = "round_robin"
	AlgorithmSourceHash = "source_hash"

	MinPort = 1
	MaxPort = 65535

	MaxBackends = 64
)

type Balancer struct {
	ID         string
	ProjectID  string
	Name       string
	Protocol   string
	ListenPort int
	TargetPort int
	Algorithm  string
	Backends   []Backend
	CreatedAt  time.Time
}

type Backend struct {
	InstanceID string
	Address    string
	Healthy    bool
	AddedAt    time.Time
}

type CreateParams struct {
	ProjectID  string
	Name       string
	Protocol   string
	ListenPort int
	TargetPort int
	Algorithm  string
	Instances  []string
}

type Member struct {
	ProjectID string
	Address   string
	Running   bool
}

func Protocols() []string {
	return []string{ProtocolTCP, ProtocolUDP}
}

func Algorithms() []string {
	return []string{AlgorithmRoundRobin, AlgorithmSourceHash}
}
