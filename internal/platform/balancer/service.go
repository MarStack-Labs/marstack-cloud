package balancer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Members interface {
	Member(ctx context.Context, instanceID string) (Member, error)
}

type Ports interface {
	NodePortTaken(ctx context.Context, protocol string, port int) (bool, error)
}

type clock func() time.Time

type service struct {
	repo    *repository
	members Members
	ports   Ports
	now     clock
}

func newService(repo *repository, members Members, ports Ports, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, members: members, ports: ports, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Balancer, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Balancer{}, err
	}
	if params.Protocol == "" {
		params.Protocol = ProtocolTCP
	}
	if err := validate.OneOf("protocol", params.Protocol, Protocols()...); err != nil {
		return Balancer{}, err
	}
	if params.Algorithm == "" {
		params.Algorithm = AlgorithmRoundRobin
	}
	if err := validate.OneOf("algorithm", params.Algorithm, Algorithms()...); err != nil {
		return Balancer{}, err
	}
	if err := checkPort("target_port", params.TargetPort); err != nil {
		return Balancer{}, err
	}
	if params.ListenPort == 0 {
		params.ListenPort = params.TargetPort
	}
	if err := checkPort("listen_port", params.ListenPort); err != nil {
		return Balancer{}, err
	}
	if len(params.Instances) > MaxBackends {
		return Balancer{}, fault.Invalid("too_many_backends", fmt.Sprintf(
			"a balancer takes at most %d backends", MaxBackends))
	}

	if err := s.claimPort(ctx, params.Protocol, params.ListenPort); err != nil {
		return Balancer{}, err
	}

	at := s.now()
	b := Balancer{
		ID:         ids.New("lb"),
		ProjectID:  params.ProjectID,
		Name:       params.Name,
		Protocol:   params.Protocol,
		ListenPort: params.ListenPort,
		TargetPort: params.TargetPort,
		Algorithm:  params.Algorithm,
		CreatedAt:  at,
	}

	seen := map[string]bool{}
	for _, id := range params.Instances {
		if seen[id] {
			continue
		}
		seen[id] = true

		if err := s.checkMember(ctx, params.ProjectID, id); err != nil {
			return Balancer{}, err
		}
		b.Backends = append(b.Backends, Backend{InstanceID: id, AddedAt: at})
	}

	if err := s.repo.insert(ctx, b); err != nil {
		return Balancer{}, s.translatePort(b, err)
	}
	return s.withHealth(ctx, b), nil
}

func (s *service) claimPort(ctx context.Context, protocol string, port int) error {
	taken, err := s.repo.portTaken(ctx, protocol, port)
	if err != nil {
		return err
	}
	if taken {
		return conflictOn(protocol, port, "another balancer")
	}

	if s.ports == nil {
		return nil
	}
	published, err := s.ports.NodePortTaken(ctx, protocol, port)
	if err != nil {
		return err
	}
	if published {
		return conflictOn(protocol, port, "a published port")
	}
	return nil
}

func conflictOn(protocol string, port int, holder string) error {
	return fault.Conflict("listen_port_taken", "port "+strconv.Itoa(port)+"/"+protocol+
		" is already claimed by "+holder+
		", and a balancer claims it on every node")
}

func (s *service) checkMember(ctx context.Context, projectID, instanceID string) error {
	if s.members == nil {
		return fault.Unavailable("members_unavailable",
			"the platform cannot look up where an instance runs")
	}

	member, err := s.members.Member(ctx, instanceID)
	if err != nil {
		return fault.Invalid("unknown_backend", "no instance with id "+instanceID+" exists")
	}
	if member.ProjectID != projectID {
		return fault.Invalid("unknown_backend", "no instance with id "+instanceID+" exists")
	}
	return nil
}

func (s *service) addBackend(ctx context.Context, projectID, id, instanceID string) (Balancer, error) {
	b, err := s.find(ctx, projectID, id)
	if err != nil {
		return Balancer{}, err
	}
	if err := s.checkMember(ctx, projectID, instanceID); err != nil {
		return Balancer{}, err
	}

	count, err := s.repo.countBackends(ctx, b.ID)
	if err != nil {
		return Balancer{}, err
	}
	if count >= MaxBackends {
		return Balancer{}, fault.Conflict("too_many_backends", fmt.Sprintf(
			"this balancer already holds the maximum of %d backends", MaxBackends))
	}

	if err := s.repo.addBackend(ctx, b.ID, instanceID, s.now()); err != nil {
		return Balancer{}, translate(err)
	}
	return s.get(ctx, projectID, b.ID)
}

func (s *service) removeBackend(ctx context.Context, projectID, id, instanceID string) error {
	b, err := s.find(ctx, projectID, id)
	if err != nil {
		return err
	}
	if err := s.repo.removeBackend(ctx, b.ID, instanceID); err != nil {
		if errors.Is(err, errNotFound) {
			return fault.NotFound("backend_not_found",
				"that instance is not a backend of this balancer")
		}
		return translate(err)
	}
	return nil
}

func (s *service) get(ctx context.Context, projectID, id string) (Balancer, error) {
	b, err := s.find(ctx, projectID, id)
	if err != nil {
		return Balancer{}, err
	}
	return s.withHealth(ctx, b), nil
}

func (s *service) find(ctx context.Context, projectID, id string) (Balancer, error) {
	b, err := s.repo.byID(ctx, projectID, id)
	if errors.Is(err, errNotFound) {
		b, err = s.repo.byName(ctx, projectID, id)
	}
	if err != nil {
		return Balancer{}, translate(err)
	}
	return b, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Balancer, error) {
	balancers, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	for i := range balancers {
		balancers[i] = s.withHealth(ctx, balancers[i])
	}
	return balancers, nil
}

func (s *service) forNode(ctx context.Context) ([]Balancer, error) {
	balancers, err := s.repo.all(ctx)
	if err != nil {
		return nil, translate(err)
	}

	live := make([]Balancer, 0, len(balancers))
	for _, b := range balancers {
		b = s.withHealth(ctx, b)
		b.Backends = healthy(b.Backends)
		if len(b.Backends) == 0 {
			continue
		}
		live = append(live, b)
	}
	return live, nil
}

func healthy(backends []Backend) []Backend {
	up := make([]Backend, 0, len(backends))
	for _, backend := range backends {
		if backend.Healthy && backend.Address != "" {
			up = append(up, backend)
		}
	}
	return up
}

func (s *service) withHealth(ctx context.Context, b Balancer) Balancer {
	if s.members == nil {
		return b
	}
	for i := range b.Backends {
		member, err := s.members.Member(ctx, b.Backends[i].InstanceID)
		if err != nil {
			continue
		}
		b.Backends[i].Address = member.Address
		b.Backends[i].Healthy = member.Running && member.Address != ""
	}
	return b
}

func (s *service) remove(ctx context.Context, projectID, id string) error {
	b, err := s.find(ctx, projectID, id)
	if err != nil {
		return err
	}
	if err := s.repo.delete(ctx, b.ID); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) releaseInstance(ctx context.Context, instanceID string) error {
	if err := s.repo.releaseInstance(ctx, instanceID); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) listenPortTaken(ctx context.Context, protocol string, port int) (bool, error) {
	return s.repo.portTaken(ctx, protocol, port)
}

func checkPort(field string, port int) error {
	if port < MinPort || port > MaxPort {
		return fault.Invalid("invalid_"+field, fmt.Sprintf(
			"%s must be between %d and %d", field, MinPort, MaxPort))
	}
	return nil
}

func (s *service) translatePort(b Balancer, err error) error {
	if errors.Is(err, errPortUsed) {
		return conflictOn(b.Protocol, b.ListenPort, "another balancer")
	}
	return translate(err)
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("balancer_not_found", "no balancer with that name or id exists")
	case errors.Is(err, errNameUsed):
		return fault.Conflict("balancer_name_taken",
			"a balancer with that name already exists in this project")
	case errors.Is(err, errPortUsed):
		return fault.Conflict("listen_port_taken", "that listen port is already claimed")
	default:
		return err
	}
}
