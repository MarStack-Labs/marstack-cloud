package balancer

import "time"

const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"

	AlgorithmRoundRobin = "round_robin"
	AlgorithmSourceHash = "source_hash"

	CheckNone = "none"
	CheckTCP  = "tcp"
	CheckHTTP = "http"

	ProbePassing = "passing"
	ProbeFailing = "failing"
	ProbeUnknown = "unknown"

	MinPort = 1
	MaxPort = 65535

	MaxBackends   = 64
	MaxThreshold  = 10
	DefaultRise   = 2
	DefaultFall   = 2
	MaxPathLength = 200

	HealthGrace = 90 * time.Second
)

type Balancer struct {
	ID         string
	ProjectID  string
	Name       string
	Protocol   string
	ListenPort int
	TargetPort int
	Algorithm  string
	Check      string
	CheckPath  string
	Rise       int
	Fall       int
	Backends   []Backend
	CreatedAt  time.Time
}

type Backend struct {
	InstanceID string
	Address    string
	Running    bool
	Healthy    bool
	Probe      string
	Reason     string
	CheckedAt  time.Time
	AddedAt    time.Time
}

type CreateParams struct {
	ProjectID  string
	Name       string
	Protocol   string
	ListenPort int
	TargetPort int
	Algorithm  string
	Check      string
	CheckPath  string
	Rise       int
	Fall       int
	Instances  []string
}

type Member struct {
	ProjectID string
	NodeID    string
	Address   string
	Running   bool
}

type Report struct {
	BalancerID string
	InstanceID string
	Healthy    bool
	Reason     string
}

func Protocols() []string {
	return []string{ProtocolTCP, ProtocolUDP}
}

func Algorithms() []string {
	return []string{AlgorithmRoundRobin, AlgorithmSourceHash}
}

func Checks() []string {
	return []string{CheckNone, CheckTCP, CheckHTTP}
}
