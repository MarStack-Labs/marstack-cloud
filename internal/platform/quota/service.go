package quota

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

const MaxLimit = 1 << 24

type Usage interface {
	InProject(ctx context.Context, projectID string) (Consumed, error)
}

type Projects interface {
	Exists(ctx context.Context, id string) (bool, error)
}

type clock func() time.Time

type service struct {
	repo     *repository
	usage    Usage
	projects Projects
	now      clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) set(ctx context.Context, projectID string, limits Limits) (Quota, error) {
	for _, d := range limits.against(Consumed{}, Claim{}) {
		if d.limit < 0 {
			return Quota{}, fault.Invalid("invalid_limit",
				"the limit on "+d.name+" cannot be negative")
		}
		if d.limit > MaxLimit {
			return Quota{}, fault.Invalid("invalid_limit",
				"a limit on "+d.name+" beyond "+strconv.Itoa(MaxLimit)+" is the same as none")
		}
	}

	if s.projects == nil {
		return Quota{}, fault.Unavailable("projects_unavailable",
			"the platform cannot confirm the project exists")
	}
	exists, err := s.projects.Exists(ctx, projectID)
	if err != nil {
		return Quota{}, fault.Internal(err)
	}
	if !exists {
		return Quota{}, fault.NotFound("project_not_found", "no project with that id exists")
	}

	q := Quota{ProjectID: projectID, Limits: limits, UpdatedAt: s.now()}
	if err := s.repo.upsert(ctx, q); err != nil {
		return Quota{}, fault.Internal(err)
	}
	return q, nil
}

func (s *service) limitsOf(ctx context.Context, projectID string) (Limits, error) {
	q, err := s.repo.byProject(ctx, projectID)
	if errors.Is(err, errNotFound) {
		return Limits{}, nil
	}
	if err != nil {
		return Limits{}, fault.Internal(err)
	}
	return q.Limits, nil
}

func (s *service) report(ctx context.Context, projectID string) (Report, error) {
	limits, err := s.limitsOf(ctx, projectID)
	if err != nil {
		return Report{}, err
	}

	consumed, err := s.consumed(ctx, projectID)
	if err != nil {
		return Report{}, err
	}

	report := Report{ProjectID: projectID, Limits: limits, Consumed: consumed}
	if q, err := s.repo.byProject(ctx, projectID); err == nil {
		report.UpdatedAt = q.UpdatedAt
	}
	return report, nil
}

func (s *service) reports(ctx context.Context) ([]Report, error) {
	quotas, err := s.repo.list(ctx)
	if err != nil {
		return nil, fault.Internal(err)
	}

	reports := make([]Report, 0, len(quotas))
	for _, q := range quotas {
		consumed, err := s.consumed(ctx, q.ProjectID)
		if err != nil {
			return nil, err
		}
		reports = append(reports, Report{
			ProjectID: q.ProjectID,
			Limits:    q.Limits,
			Consumed:  consumed,
			UpdatedAt: q.UpdatedAt,
		})
	}
	return reports, nil
}

func (s *service) consumed(ctx context.Context, projectID string) (Consumed, error) {
	if s.usage == nil {
		return Consumed{}, fault.Unavailable("usage_unavailable",
			"the platform cannot measure what a project already holds")
	}

	consumed, err := s.usage.InProject(ctx, projectID)
	if err != nil {
		return Consumed{}, fault.Internal(err)
	}
	return consumed, nil
}

func (s *service) admit(ctx context.Context, projectID string, claim Claim) error {
	limits, err := s.limitsOf(ctx, projectID)
	if err != nil {
		return err
	}
	if limits.Empty() {
		return nil
	}

	consumed, err := s.consumed(ctx, projectID)
	if err != nil {
		return err
	}

	for _, d := range limits.against(consumed, claim) {
		if d.limit == Unlimited || d.claim == 0 {
			continue
		}
		if d.consumed+d.claim > d.limit {
			return fault.Conflict("quota_exceeded", refusal(d))
		}
	}
	return nil
}

func refusal(d dimension) string {
	unit := d.unit
	if unit != "" {
		unit = " " + unit
	}
	return fmt.Sprintf("the project is limited to %d%s of %s and already holds %d%s, "+
		"so %d%s more would not fit",
		d.limit, unit, d.name, d.consumed, unit, d.claim, unit)
}

func (s *service) remove(ctx context.Context, projectID string) error {
	if err := s.repo.delete(ctx, projectID); err != nil {
		if errors.Is(err, errNotFound) {
			return fault.NotFound("quota_not_found", "that project has no limits set")
		}
		return fault.Internal(err)
	}
	return nil
}
