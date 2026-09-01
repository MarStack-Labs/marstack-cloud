package autoscale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
)

type Services interface {
	GroupOf(ctx context.Context, projectID, serviceID string) (Group, error)
	Groups(ctx context.Context) ([]Group, error)
	Scale(ctx context.Context, projectID, serviceID string, replicas int) error
}

type Load interface {
	SamplesOf(ctx context.Context) (map[string]Sample, error)
}

type clock func() time.Time

type service struct {
	repo     *repository
	services Services
	load     Load
	events   events.Recorder
	log      *slog.Logger
	now      clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
}

func (s *service) set(ctx context.Context, params PolicyParams) (Policy, error) {
	if s.services == nil {
		return Policy{}, fault.NotFound("service_not_found", "no service with that id exists")
	}

	group, err := s.services.GroupOf(ctx, params.ProjectID, params.ServiceID)
	if err != nil {
		return Policy{}, err
	}

	if params.Min < MinReplicas || params.Min > MaxReplicas {
		return Policy{}, fault.Invalid("invalid_min", fmt.Sprintf(
			"min must be between %d and %d", MinReplicas, MaxReplicas))
	}
	if params.Max < params.Min || params.Max > MaxReplicas {
		return Policy{}, fault.Invalid("invalid_max", fmt.Sprintf(
			"max must be between min and %d", MaxReplicas))
	}
	if params.TargetCPU == 0 && params.TargetMemory == 0 {
		return Policy{}, fault.Invalid("invalid_target",
			"name a target cpu percent, a target memory percent, or both: a policy with "+
				"neither has nothing to scale against")
	}
	for _, named := range []struct {
		name   string
		target int
	}{{"cpu", params.TargetCPU}, {"memory", params.TargetMemory}} {
		name, target := named.name, named.target
		if target == 0 {
			continue
		}
		if target < MinTarget || target > MaxTarget {
			return Policy{}, fault.Invalid("invalid_target", fmt.Sprintf(
				"the target %s percent must be between %d and %d",
				name, MinTarget, MaxTarget))
		}
	}

	at := s.now()
	policy := Policy{
		ID:           ids.New("as"),
		ProjectID:    params.ProjectID,
		ServiceID:    group.ServiceID,
		Min:          params.Min,
		Max:          params.Max,
		TargetCPU:    params.TargetCPU,
		TargetMemory: params.TargetMemory,
		CreatedAt:    at,
		UpdatedAt:    at,
	}

	if err := s.repo.upsert(ctx, policy); err != nil {
		return Policy{}, err
	}
	return s.policyOf(ctx, params.ProjectID, group.ServiceID)
}

func (s *service) policyOf(ctx context.Context, projectID, serviceID string) (Policy, error) {
	if s.services != nil {
		group, err := s.services.GroupOf(ctx, projectID, serviceID)
		if err != nil {
			return Policy{}, err
		}
		serviceID = group.ServiceID
	}

	policy, err := s.repo.byService(ctx, serviceID)
	if errors.Is(err, errNotFound) {
		return Policy{}, fault.NotFound("autoscale_not_found",
			"this service does not scale itself")
	}
	if err != nil {
		return Policy{}, err
	}
	if policy.ProjectID != projectID {
		return Policy{}, fault.NotFound("autoscale_not_found",
			"this service does not scale itself")
	}
	return policy, nil
}

func (s *service) clear(ctx context.Context, projectID, serviceID string) error {
	policy, err := s.policyOf(ctx, projectID, serviceID)
	if err != nil {
		return err
	}
	return s.repo.delete(ctx, policy.ID)
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Policy, error) {
	return s.repo.listIn(ctx, projectID)
}

func (s *service) sweep(ctx context.Context) (int, error) {
	if s.services == nil || s.load == nil {
		return 0, nil
	}

	policies, err := s.repo.all(ctx)
	if err != nil {
		return 0, err
	}
	if len(policies) == 0 {
		return 0, nil
	}

	groups, err := s.services.Groups(ctx)
	if err != nil {
		return 0, err
	}
	byService := make(map[string]Group, len(groups))
	for _, group := range groups {
		byService[group.ServiceID] = group
	}

	samples, err := s.load.SamplesOf(ctx)
	if err != nil {
		return 0, err
	}

	changed := 0
	for _, policy := range policies {
		group, held := byService[policy.ServiceID]
		if !held {
			if err := s.repo.delete(ctx, policy.ID); err != nil {
				s.log.Warn("could not drop a policy for a service that is gone",
					"policy", policy.ID, "error", err)
			}
			continue
		}

		if s.apply(ctx, policy, group, samples) {
			changed++
		}
	}
	return changed, nil
}

func (s *service) apply(ctx context.Context, policy Policy, group Group,
	samples map[string]Sample) bool {
	decision := decide(policy, group, samples, s.now())

	if !decision.Act {
		if decision.Reason != policy.LastReason {
			if err := s.repo.setReason(ctx, policy.ID, decision.Reason); err != nil {
				s.log.Warn("could not record why nothing changed",
					"policy", policy.ID, "error", err)
			}
		}
		return false
	}

	if err := s.services.Scale(ctx, policy.ProjectID, policy.ServiceID,
		decision.Replicas); err != nil {
		s.log.Warn("could not scale a service", "service", group.Name, "error", err)
		return false
	}

	if err := s.repo.setDecision(ctx, policy.ID, decision.Reason, s.now()); err != nil {
		s.log.Warn("could not record a scaling decision", "policy", policy.ID, "error", err)
	}

	direction := "up"
	if decision.Replicas < group.Replicas {
		direction = "down"
	}
	s.note(ctx, policy, events.Entry{
		Kind:    "service.scaled",
		Subject: policy.ServiceID,
		Message: group.Name + " scaled " + direction + " from " +
			strconv.Itoa(group.Replicas) + " to " + strconv.Itoa(decision.Replicas) +
			": " + decision.Reason,
		Severity: events.Info,
	})
	return true
}

func (s *service) note(ctx context.Context, policy Policy, entry events.Entry) {
	if s.events == nil {
		return
	}
	entry.ProjectID = policy.ProjectID
	s.events.Record(ctx, entry)
}
