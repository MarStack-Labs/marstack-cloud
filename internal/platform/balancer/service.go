package balancer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Members interface {
	Member(ctx context.Context, instanceID string) (Member, error)
}

type Ports interface {
	NodePortTaken(ctx context.Context, protocol string, port int) (bool, error)
}

type Services interface {
	MembersOf(ctx context.Context, projectID, serviceID string) ([]string, error)
}

type clock func() time.Time

type service struct {
	repo     *repository
	members  Members
	ports    Ports
	services Services
	events   events.Recorder
	sealing  *sealed.Keyring
	now      clock
}

func (s *service) certificateOf(b Balancer) (string, string, error) {
	return openCertificate(b.TLS, s.sealing)
}

func (s *service) setCertificate(
	ctx context.Context, id, projectID, certPEM, keyPEM string,
) (Balancer, error) {
	b, err := s.get(ctx, projectID, id)
	if err != nil {
		return Balancer{}, err
	}

	var carried TLS
	if certPEM != "" || keyPEM != "" {
		if carried, err = sealCertificate(certPEM, keyPEM, s.sealing); err != nil {
			return Balancer{}, err
		}
	}

	if err := s.repo.setTLS(ctx, b.ID, carried); err != nil {
		return Balancer{}, translate(err)
	}
	return s.get(ctx, projectID, id)
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
	if params.Check == "" {
		params.Check = CheckNone
	}
	if err := validate.OneOf("check", params.Check, Checks()...); err != nil {
		return Balancer{}, err
	}
	if err := checkTarget(&params); err != nil {
		return Balancer{}, err
	}
	if err := thresholds(&params); err != nil {
		return Balancer{}, err
	}
	if params.ServiceID != "" && len(params.Instances) > 0 {
		return Balancer{}, fault.Invalid("backends_unused",
			"a balancer that follows a service takes its backends from that service, so naming "+
				"instances as well would leave two sets of expectations")
	}
	if params.ServiceID != "" {
		if s.services == nil {
			return Balancer{}, fault.Unavailable("services_unavailable",
				"the platform cannot look up a service")
		}
		if _, err := s.services.MembersOf(ctx, params.ProjectID, params.ServiceID); err != nil {
			return Balancer{}, fault.Invalid("unknown_service",
				"no service named "+params.ServiceID+" exists in this project")
		}
	}
	if len(params.Instances) > MaxBackends {
		return Balancer{}, fault.Invalid("too_many_backends", fmt.Sprintf(
			"a balancer takes at most %d backends", MaxBackends))
	}

	if err := s.claimPort(ctx, params.Protocol, params.ListenPort); err != nil {
		return Balancer{}, err
	}

	at := s.now()
	if params.Family == "" {
		params.Family = FamilyIPv4
	}
	if err := validate.OneOf("family", params.Family, Families()...); err != nil {
		return Balancer{}, err
	}

	b := Balancer{
		ID:         ids.New("lb"),
		ProjectID:  params.ProjectID,
		Name:       params.Name,
		Protocol:   params.Protocol,
		ListenPort: params.ListenPort,
		TargetPort: params.TargetPort,
		Algorithm:  params.Algorithm,
		ServiceID:  params.ServiceID,
		Check:      params.Check,
		CheckPath:  params.CheckPath,
		Rise:       params.Rise,
		Fall:       params.Fall,
		Family:     params.Family,
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
	return s.resolve(ctx, b)
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
	if b.ServiceID != "" {
		return Balancer{}, ownedByService(b)
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
	if b.ServiceID != "" {
		return ownedByService(b)
	}
	if err := s.repo.forgetHealth(ctx, b.ID, instanceID); err != nil {
		return translate(err)
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

func ownedByService(b Balancer) error {
	return fault.Conflict("backends_owned_by_service",
		"the backends of "+b.Name+" come from service "+b.ServiceID+
			", so scale that service instead")
}

func (s *service) membership(ctx context.Context, b Balancer) (Balancer, error) {
	if b.ServiceID == "" || s.services == nil {
		return b, nil
	}

	ids, err := s.services.MembersOf(ctx, b.ProjectID, b.ServiceID)
	if err != nil {
		b.Backends = nil
		return b, nil
	}

	health, err := s.repo.healthOf(ctx, b.ID)
	if err != nil {
		return b, err
	}

	backends := make([]Backend, 0, len(ids))
	for _, id := range ids {
		backend := Backend{InstanceID: id}
		if known, seen := health[id]; seen {
			backend.Probe = known.Probe
			backend.Reason = known.Reason
			backend.CheckedAt = known.CheckedAt
		}
		backends = append(backends, backend)
	}
	b.Backends = backends
	return b, nil
}

func (s *service) get(ctx context.Context, projectID, id string) (Balancer, error) {
	b, err := s.find(ctx, projectID, id)
	if err != nil {
		return Balancer{}, err
	}
	return s.resolve(ctx, b)
}

func (s *service) resolve(ctx context.Context, b Balancer) (Balancer, error) {
	held, err := s.membership(ctx, b)
	if err != nil {
		return Balancer{}, err
	}
	return s.withHealth(ctx, held), nil
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
		resolved, err := s.resolve(ctx, balancers[i])
		if err != nil {
			return nil, err
		}
		balancers[i] = resolved
	}
	return balancers, nil
}

func (s *service) forNode(ctx context.Context) ([]Balancer, error) {
	balancers, err := s.repo.all(ctx)
	if err != nil {
		return nil, translate(err)
	}

	for i := range balancers {
		resolved, err := s.resolve(ctx, balancers[i])
		if err != nil {
			return nil, err
		}
		balancers[i] = resolved
	}
	return balancers, nil
}

func (s *service) withHealth(ctx context.Context, b Balancer) Balancer {
	if s.members == nil {
		return b
	}

	for i := range b.Backends {
		backend := &b.Backends[i]

		member, err := s.members.Member(ctx, backend.InstanceID)
		if err != nil {
			continue
		}
		backend.Address = member.AddressIn(b.Family)
		backend.Running = member.Running && backend.Address != ""

		if b.Check == CheckNone {
			backend.Healthy = backend.Running
			continue
		}

		if backend.Probe == "" {
			backend.Probe = ProbeUnknown
			backend.Reason = "no report from the node holding it yet"
		} else if s.now().Sub(backend.CheckedAt) > HealthGrace {
			backend.Probe = ProbeUnknown
			backend.Reason = "the last report is older than " + HealthGrace.String()
		}
		backend.Healthy = backend.Running && backend.Probe == ProbePassing
	}
	return b
}

func (s *service) heldBy(ctx context.Context, cache map[string]bool, nodeID, instanceID string) bool {
	if s.members == nil {
		return false
	}
	if verdict, seen := cache[instanceID]; seen {
		return verdict
	}

	member, err := s.members.Member(ctx, instanceID)
	verdict := err == nil && member.NodeID != "" && member.NodeID == nodeID
	cache[instanceID] = verdict
	return verdict
}

func (s *service) reportHealth(ctx context.Context, nodeID string, reports []Report) error {
	if len(reports) == 0 {
		return nil
	}
	if len(reports) > MaxBackends*MaxBackends {
		return fault.Invalid("too_many_reports", "that is more reports than a node could hold")
	}

	known, err := s.repo.all(ctx)
	if err != nil {
		return translate(err)
	}

	members := map[string]bool{}
	for _, b := range known {
		held, err := s.membership(ctx, b)
		if err != nil {
			return translate(err)
		}
		for _, backend := range held.Backends {
			members[b.ID+"/"+backend.InstanceID] = true
		}
	}

	held := map[string]bool{}
	wanted := make([]Report, 0, len(reports))
	for _, report := range reports {
		if !members[report.BalancerID+"/"+report.InstanceID] {
			continue
		}
		if !s.heldBy(ctx, held, nodeID, report.InstanceID) {
			continue
		}
		if len(report.Reason) > MaxPathLength {
			report.Reason = report.Reason[:MaxPathLength]
		}
		wanted = append(wanted, report)
	}
	if len(wanted) == 0 {
		return nil
	}

	was := map[string]Backend{}
	for _, b := range known {
		held, err := s.membership(ctx, b)
		if err != nil {
			return translate(err)
		}
		for _, backend := range held.Backends {
			was[b.ID+"/"+backend.InstanceID] = backend
		}
	}

	if err := s.repo.saveHealth(ctx, wanted, s.now()); err != nil {
		return translate(err)
	}

	for _, report := range wanted {
		s.noteFlip(ctx, was[report.BalancerID+"/"+report.InstanceID], report)
	}
	return nil
}

func (s *service) noteFlip(ctx context.Context, before Backend, report Report) {
	if s.events == nil {
		return
	}

	after := ProbeFailing
	if report.Healthy {
		after = ProbePassing
	}
	if before.Probe == after {
		return
	}

	entry := events.Entry{
		Subject:  report.InstanceID,
		Message:  "balancer " + report.BalancerID + ": " + report.Reason,
		Kind:     "backend.down",
		Severity: events.Warn,
	}
	if report.Healthy {
		entry.Kind = "backend.up"
		entry.Severity = events.Info
		entry.Message = "balancer " + report.BalancerID + ": the probe passes"
	}

	if member, err := s.members.Member(ctx, report.InstanceID); err == nil {
		entry.ProjectID = member.ProjectID
		entry.NodeID = member.NodeID
	}
	s.events.Record(ctx, entry)
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

func checkTarget(params *CreateParams) error {
	if params.Check != CheckHTTP {
		if params.CheckPath != "" {
			return fault.Invalid("check_path_unused",
				"check_path only means something for an http check")
		}
		return nil
	}

	if params.CheckPath == "" {
		params.CheckPath = "/"
	}
	if !strings.HasPrefix(params.CheckPath, "/") {
		return fault.Invalid("invalid_check_path", "check_path must start with /")
	}
	if len(params.CheckPath) > MaxPathLength {
		return fault.Invalid("invalid_check_path", fmt.Sprintf(
			"check_path must be at most %d characters", MaxPathLength))
	}
	if strings.ContainsAny(params.CheckPath, " \t\r\n") {
		return fault.Invalid("invalid_check_path", "check_path must not contain whitespace")
	}
	if params.Protocol != ProtocolTCP {
		return fault.Invalid("check_unsupported",
			"an http check needs a tcp balancer, and this one is "+params.Protocol)
	}
	return nil
}

func thresholds(params *CreateParams) error {
	if params.Check == CheckNone {
		if params.Rise != 0 || params.Fall != 0 {
			return fault.Invalid("thresholds_unused",
				"rise and fall only mean something once a check is set")
		}
		return nil
	}

	if params.Rise == 0 {
		params.Rise = DefaultRise
	}
	if params.Fall == 0 {
		params.Fall = DefaultFall
	}
	for field, value := range map[string]int{"rise": params.Rise, "fall": params.Fall} {
		if value < 1 || value > MaxThreshold {
			return fault.Invalid("invalid_"+field, fmt.Sprintf(
				"%s must be between 1 and %d", field, MaxThreshold))
		}
	}
	return nil
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
