package instance

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/page"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type clock func() time.Time

type service struct {
	repo      *repository
	now       clock
	networks  Networks
	keys      Keys
	quota     Quota
	volumes   Volumes
	forwards  Forwards
	balancers Balancers
	events    events.Recorder
	firewalls Firewalls
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Instance, error) {
	normalized, err := normalize(params)
	if err != nil {
		return Instance{}, err
	}

	authorized, err := s.authorizedKeys(ctx, params, normalized)
	if err != nil {
		return Instance{}, err
	}

	if s.quota != nil {
		if err := s.quota.AdmitInstance(ctx, params.ProjectID,
			normalized.VCPU, normalized.MemoryMiB); err != nil {
			return Instance{}, err
		}
	}

	networkID := normalized.NetworkID
	if networkID == "" && s.networks != nil {
		resolved, err := s.networks.DefaultNetworkID(ctx, params.ProjectID)
		if err != nil {
			return Instance{}, err
		}
		networkID = resolved
	}

	if normalized.FirewallID != "" && s.firewalls != nil {
		known, err := s.firewalls.ExistsIn(ctx, normalized.FirewallID, params.ProjectID)
		if err != nil {
			return Instance{}, err
		}
		if !known {
			return Instance{}, fault.Invalid("unknown_firewall",
				"no firewall with that id exists, and a typo here would silently mean no rules")
		}
	}

	now := s.now()
	in := Instance{
		ID:            ids.New("i"),
		ProjectID:     params.ProjectID,
		Group:         normalized.Group,
		Strict:        params.Strict,
		SSHKeys:       authorized,
		Name:          normalized.Name,
		Isolation:     Isolation(normalized.Isolation),
		Image:         normalized.Image,
		ISO:           normalized.ISO,
		Kernel:        normalized.Kernel,
		DiskGiB:       normalized.DiskGiB,
		FirewallID:    normalized.FirewallID,
		Command:       normalized.Command,
		NetworkID:     networkID,
		RestartPolicy: RestartPolicy(normalized.RestartPolicy),
		VCPU:          normalized.VCPU,
		MemoryMiB:     normalized.MemoryMiB,
		Desired:       DesiredRunning,
		Observed:      ObservedPending,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.repo.insert(ctx, in); err != nil {
		return Instance{}, translate(err)
	}
	return in, nil
}

func (s *service) get(ctx context.Context, id string) (Instance, error) {
	in, err := s.repo.get(ctx, id)
	if err != nil {
		return Instance{}, translate(err)
	}
	return in, nil
}

func (s *service) list(ctx context.Context) ([]Instance, error) {
	instances, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return instances, nil
}

func (s *service) footprintIn(ctx context.Context, projectID string) (Footprint, error) {
	footprint, err := s.repo.footprintIn(ctx, projectID)
	if err != nil {
		return Footprint{}, translate(err)
	}
	return footprint, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Instance, error) {
	instances, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return instances, nil
}

func (s *service) pageIn(
	ctx context.Context, projectID string, window page.Window,
) ([]Instance, error) {
	instances, err := s.repo.pageIn(ctx, projectID, window)
	if err != nil {
		return nil, translate(err)
	}
	return instances, nil
}

func (s *service) getIn(ctx context.Context, id, projectID string) (Instance, error) {
	in, err := s.get(ctx, id)
	if err != nil {
		return Instance{}, err
	}
	if in.ProjectID != projectID {
		return Instance{}, fault.NotFound("instance_not_found", "no instance with that id exists")
	}
	return in, nil
}

func (s *service) setDesired(
	ctx context.Context, id, projectID string, desired DesiredState,
) (Instance, error) {
	if _, err := s.getIn(ctx, id, projectID); err != nil {
		return Instance{}, err
	}
	if err := s.repo.setDesired(ctx, id, desired, s.now()); err != nil {
		return Instance{}, translate(err)
	}
	return s.get(ctx, id)
}

func (s *service) delete(ctx context.Context, id, projectID string) error {
	if _, err := s.getIn(ctx, id, projectID); err != nil {
		return err
	}
	if err := s.repo.delete(ctx, id); err != nil {
		return translate(err)
	}
	if s.networks == nil {
		return nil
	}
	if err := s.networks.ReleaseAddress(ctx, id); err != nil {
		return fault.Internal(fmt.Errorf("instance %s was deleted but its address was not released: %w", id, err))
	}
	if s.volumes != nil {
		if err := s.volumes.ReleaseInstance(ctx, id); err != nil {
			return fault.Internal(fmt.Errorf(
				"instance %s was deleted but its volumes stayed attached to it: %w", id, err))
		}
	}
	if s.forwards != nil {
		if err := s.forwards.ReleaseInstance(ctx, id); err != nil {
			return fault.Internal(fmt.Errorf(
				"instance %s was deleted but its published ports stayed: %w", id, err))
		}
	}
	if s.balancers != nil {
		if err := s.balancers.ReleaseInstance(ctx, id); err != nil {
			return fault.Internal(fmt.Errorf(
				"instance %s was deleted but stayed a balancer backend: %w", id, err))
		}
	}
	return nil
}

func (s *service) listByNode(ctx context.Context, nodeID string) ([]Instance, error) {
	instances, err := s.repo.listByNode(ctx, nodeID)
	if err != nil {
		return nil, translate(err)
	}
	return instances, nil
}

func (s *service) reportObserved(
	ctx context.Context, nodeID, instanceID, observed, message string, restarts int,
) (Instance, error) {
	if err := validate.OneOf("observed_state", observed, AllObservedStates()...); err != nil {
		return Instance{}, err
	}
	if len(message) > MaxObservedMessage {
		return Instance{}, fault.Invalid("invalid_message", fmt.Sprintf(
			"message must be at most %d characters", MaxObservedMessage,
		))
	}

	if restarts < 0 {
		return Instance{}, fault.Invalid("invalid_restarts", "restarts must not be negative")
	}

	before, beforeErr := s.repo.get(ctx, instanceID)

	if err := s.repo.setObserved(
		ctx, instanceID, nodeID, ObservedState(observed), message, restarts, s.now(),
	); err != nil {
		if errors.Is(err, errNotFound) {
			return Instance{}, fault.NotFound("instance_not_on_node",
				"no instance with that id is assigned to this node")
		}
		return Instance{}, translate(err)
	}

	after, err := s.get(ctx, instanceID)
	if err != nil {
		return Instance{}, err
	}
	if beforeErr == nil {
		s.noteTransition(ctx, before, after)
	}
	return after, nil
}

func (s *service) noteTransition(ctx context.Context, before, after Instance) {
	if s.events == nil {
		return
	}
	restarted := after.RestartCount > before.RestartCount
	if before.Observed == after.Observed && !restarted {
		return
	}

	entry := events.Entry{
		ProjectID: after.ProjectID,
		Subject:   after.ID,
		NodeID:    after.NodeID,
		Message:   after.ObservedMessage,
	}

	switch {
	case restarted:
		entry.Kind = "instance.restarted"
		entry.Severity = events.Warn
		entry.Message = "restart " + strconv.Itoa(after.RestartCount) + ": " + after.ObservedMessage
	case after.Observed == ObservedRunning:
		entry.Kind = "instance.running"
		entry.Severity = events.Info
	case after.Observed == ObservedFailed:
		entry.Kind = "instance.failed"
		entry.Severity = events.Error
	case after.Observed == ObservedStopped:
		entry.Kind = "instance.stopped"
		entry.Severity = events.Info
	case after.Observed == ObservedPending:
		entry.Kind = "instance.pending"
		entry.Severity = events.Warn
	default:
		entry.Kind = "instance." + string(after.Observed)
		entry.Severity = events.Info
	}

	if entry.Message == "" {
		entry.Message = "observed " + string(before.Observed) + " to " + string(after.Observed)
	}
	s.events.Record(ctx, entry)
}

func (s *service) pendingPlacement(ctx context.Context) ([]Instance, error) {
	instances, err := s.repo.listPendingPlacement(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return instances, nil
}

func (s *service) assignedCounts(ctx context.Context) (map[string]int, error) {
	counts, err := s.repo.assignedCounts(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return counts, nil
}

func (s *service) strandedOn(ctx context.Context, nodeIDs []string) ([]Instance, error) {
	stranded := make([]Instance, 0)
	for _, nodeID := range nodeIDs {
		instances, err := s.repo.listRunningOn(ctx, nodeID)
		if err != nil {
			return nil, translate(err)
		}
		stranded = append(stranded, instances...)
	}
	return stranded, nil
}

func (s *service) releasePlacement(ctx context.Context, id, nodeID string) error {
	if err := s.repo.releasePlacement(ctx, id, nodeID, s.now()); err != nil {
		return translate(err)
	}
	if s.networks == nil {
		return nil
	}
	return s.networks.ReleaseAddress(ctx, id)
}

func (s *service) assign(ctx context.Context, id, nodeID string) error {
	return translate(s.repo.assign(ctx, id, nodeID, s.now()))
}

func normalize(params CreateParams) (CreateParams, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return params, err
	}
	if params.Group != "" {
		if err := validate.Name("placement_group", params.Group); err != nil {
			return params, err
		}
	}
	if params.Strict && params.Group == "" {
		return params, fault.Invalid("invalid_placement",
			"strict placement without a group has nothing to spread away from")
	}
	if err := validate.OneOf("isolation", params.Isolation, AllIsolations()...); err != nil {
		return params, err
	}
	if params.Image == "" && params.ISO == "" {
		return params, fault.Invalid("invalid_image",
			"image must not be empty, unless an iso is given to boot from instead")
	}
	if params.ISO != "" && params.Isolation != string(IsolationVM) {
		return params, fault.Invalid("invalid_iso",
			"only isolation vm can attach an iso, because nothing else emulates optical media")
	}
	if params.Kernel != "" &&
		params.Isolation != string(IsolationMicroVM) && params.Isolation != string(IsolationSandbox) {
		return params, fault.Invalid("invalid_kernel",
			"only microvm and sandbox boot a kernel directly")
	}
	if params.DiskGiB != 0 && params.Isolation != string(IsolationVM) {
		return params, fault.Invalid("invalid_disk",
			"only isolation vm has a disk of its own to size")
	}
	if params.Image == "" && params.DiskGiB == 0 {
		params.DiskGiB = DefaultDiskGiB
	}
	if params.DiskGiB < 0 || params.DiskGiB > MaxDiskGiB {
		return params, fault.Invalid("invalid_disk", fmt.Sprintf(
			"disk_gib must be between 1 and %d", MaxDiskGiB,
		))
	}
	if params.RestartPolicy == "" {
		params.RestartPolicy = string(DefaultRestartPolicy)
	}
	if err := validate.OneOf("restart_policy", params.RestartPolicy, AllRestartPolicies()...); err != nil {
		return params, err
	}
	if len(params.Command) > MaxCommandArgs {
		return params, fault.Invalid("invalid_command", fmt.Sprintf(
			"command must have at most %d arguments", MaxCommandArgs,
		))
	}

	if params.VCPU == 0 {
		params.VCPU = DefaultVCPU
	}
	if params.MemoryMiB == 0 {
		params.MemoryMiB = DefaultMemoryMiB
	}

	if params.VCPU < MinVCPU || params.VCPU > MaxVCPU {
		return params, fault.Invalid("invalid_vcpu", fmt.Sprintf(
			"vcpu must be between %d and %d", MinVCPU, MaxVCPU,
		))
	}
	if params.MemoryMiB < MinMemoryMiB || params.MemoryMiB > MaxMemoryMiB {
		return params, fault.Invalid("invalid_memory", fmt.Sprintf(
			"memory_mib must be between %d and %d", MinMemoryMiB, MaxMemoryMiB,
		))
	}

	return params, nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("instance_not_found", "no instance with that id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("instance_name_taken", "an instance with that name already exists")
	case errors.Is(err, errAlreadyPlaced):
		return fault.Conflict("instance_already_placed", "the instance is already placed on a node")
	default:
		return fault.Internal(err)
	}
}

func (s *service) groupCounts(ctx context.Context, group string) (map[string]int, error) {
	counts, err := s.repo.groupCounts(ctx, group)
	if err != nil {
		return nil, translate(err)
	}
	return counts, nil
}

func (s *service) holdPlacement(ctx context.Context, id, reason string) error {
	return translate(s.repo.setObservedMessage(ctx, id, reason, s.now()))
}

func (s *service) authorizedKeys(
	ctx context.Context, params, normalized CreateParams,
) ([]string, error) {
	if len(params.Keys) == 0 {
		return nil, nil
	}
	if normalized.Isolation != string(IsolationVM) {
		return nil, fault.Invalid("keys_unsupported",
			"only isolation vm boots cloud-init, so a key given to a "+normalized.Isolation+
				" would be accepted and never installed")
	}
	if s.keys == nil {
		return nil, fault.Unavailable("keys_unavailable",
			"the platform cannot look up ssh keys")
	}

	return s.keys.Resolve(ctx, params.ProjectID, params.Keys)
}
