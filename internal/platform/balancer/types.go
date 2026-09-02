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
	MaxRoutes     = 32
	MaxHostLength = 253

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
	ServiceID  string
	Family     string
	Check      string
	CheckPath  string
	Rise       int
	Fall       int
	Backends   []Backend
	Routes     []Route
	TLS        TLS
	CreatedAt  time.Time
}

type Route struct {
	Host      string
	Path      string
	ServiceID string
	Backends  []Backend
}

type TLS struct {
	Material  string
	KeyID     string
	Subject   string
	ExpiresAt string
}

func (t TLS) Present() bool {
	return t.Material != ""
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
	ServiceID  string
	Family     string
	Check      string
	CheckPath  string
	Rise       int
	Fall       int
	Instances  []string
	Routes     []RouteParams
}

type RouteParams struct {
	Host    string
	Path    string
	Service string
}

type Member struct {
	ProjectID string
	NodeID    string
	Address   string
	Address6  string
	Running   bool
}

const (
	FamilyIPv4 = "ipv4"
	FamilyIPv6 = "ipv6"
)

func Families() []string {
	return []string{FamilyIPv4, FamilyIPv6}
}

func (m Member) AddressIn(family string) string {
	if family == FamilyIPv6 {
		return m.Address6
	}
	return m.Address
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
