package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

const MaxSamples = 512

type clock func() time.Time

type service struct {
	repo      *repository
	workloads Workloads
	log       *slog.Logger
	now       clock

	mu   sync.Mutex
	seen int
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
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
	s.accumulate(ctx, report)
	return nil
}

func (s *service) nodes(ctx context.Context) ([]NodeSample, error) {
	return s.repo.nodes(ctx)
}

func (s *service) instancesIn(ctx context.Context, projectID string) ([]InstanceSample, error) {
	if s.workloads == nil {
		return nil, fault.Unavailable("workloads_unavailable",
			"the platform cannot tell which project an instance belongs to")
	}

	held, err := s.workloads.IDsIn(ctx, projectID)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(held))
	for _, id := range held {
		wanted[id] = true
	}

	all, err := s.repo.instances(ctx)
	if err != nil {
		return nil, err
	}

	mine := make([]InstanceSample, 0, len(all))
	for _, sample := range all {
		if wanted[sample.InstanceID] {
			mine = append(mine, sample)
		}
	}
	return mine, nil
}

func bucketOf(at time.Time) int64 {
	return at.Unix() / int64(BucketSize/time.Second)
}

func (s *service) accumulate(ctx context.Context, report Report) {
	bucket := bucketOf(report.Node.ReportedAt)

	subjects := make([]string, 0, len(report.Instances)+1)
	samples := make([]Bucket, 0, len(report.Instances)+1)

	subjects = append(subjects, report.Node.NodeID)
	samples = append(samples, Bucket{
		CPUPeak:    report.Node.CPUPercent,
		MemoryPeak: report.Node.MemoryUsedMiB,
		MemoryMiB:  report.Node.MemoryMiB,
	})

	for _, sample := range report.Instances {
		subjects = append(subjects, sample.InstanceID)
		samples = append(samples, Bucket{
			CPUPeak:    sample.CPUPercent,
			MemoryPeak: sample.MemoryUsedMiB,
		})
	}

	if err := s.repo.accumulate(ctx, bucket, samples, subjects); err != nil {
		s.log.Warn("could not add to the usage history", "error", err)
		return
	}
	if !s.dueForPrune() {
		return
	}
	if err := s.repo.pruneHistory(ctx, bucket-HistoryBuckets); err != nil {
		s.log.Warn("could not prune the usage history", "error", err)
	}
}

func (s *service) dueForPrune() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seen++
	if s.seen < PruneEvery {
		return false
	}
	s.seen = 0
	return true
}

func (s *service) historyOf(ctx context.Context, subject string, window time.Duration) (History, error) {
	if subject == "" {
		return History{}, fault.Invalid("invalid_subject", "name the node or instance to read")
	}
	if window <= 0 {
		window = DefaultWindow
	}
	if window > MaxWindow {
		window = MaxWindow
	}

	from := bucketOf(s.now().Add(-window))
	buckets, err := s.repo.history(ctx, subject, from)
	if err != nil {
		return History{}, err
	}
	return History{Subject: subject, Buckets: buckets}, nil
}

func (s *service) instanceHistory(
	ctx context.Context, projectID, subject string, window time.Duration,
) (History, error) {
	if s.workloads == nil {
		return History{}, fault.Unavailable("workloads_unavailable",
			"the platform cannot tell which project an instance belongs to")
	}

	held, err := s.workloads.IDsIn(ctx, projectID)
	if err != nil {
		return History{}, err
	}

	for _, id := range held {
		if id == subject {
			return s.historyOf(ctx, subject, window)
		}
	}
	return History{}, fault.NotFound("instance_not_found", "no instance with that id exists")
}

func (s *service) forget(ctx context.Context, nodeID string) error {
	return s.repo.forget(ctx, nodeID)
}
