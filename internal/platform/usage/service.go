package usage

import (
	"context"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

const MaxSamples = 512

type clock func() time.Time

type service struct {
	repo *repository
	now  clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) record(ctx context.Context, report Report) error {
	if report.Node.NodeID == "" {
		return fault.Invalid("invalid_node", "the node id must not be empty")
	}
	if len(report.Instances) > MaxSamples {
		return fault.Invalid("too_many_samples",
			"a node may report at most 512 instances in one pass")
	}

	if report.Node.CPUPercent < 0 {
		report.Node.CPUPercent = 0
	}
	report.Node.ReportedAt = s.now()

	kept := make([]InstanceSample, 0, len(report.Instances))
	for _, sample := range report.Instances {
		if sample.InstanceID == "" {
			continue
		}
		if sample.CPUPercent < 0 {
			sample.CPUPercent = 0
		}
		kept = append(kept, sample)
	}
	report.Instances = kept

	if err := s.repo.replace(ctx, report); err != nil {
		return err
	}
	return nil
}

func (s *service) nodes(ctx context.Context) ([]NodeSample, error) {
	return s.repo.nodes(ctx)
}

func (s *service) instances(ctx context.Context) ([]InstanceSample, error) {
	return s.repo.instances(ctx)
}

func (s *service) forget(ctx context.Context, nodeID string) error {
	return s.repo.forget(ctx, nodeID)
}
