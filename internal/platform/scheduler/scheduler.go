package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

const (
	DefaultInterval = 5 * time.Second

	DefaultStrandedGrace = 2 * time.Minute

	movableIsolation = "container"
)

type Candidate struct {
	ID   string
	Name string
	Zone string
}

type Pending struct {
	ID        string
	Name      string
	NetworkID string
}

type Stranded struct {
	ID        string
	Name      string
	NodeID    string
	Isolation string
}

type NodeSource interface {
	ReadyNodes(ctx context.Context) ([]Candidate, error)
	UnreachableNodes(ctx context.Context, grace time.Duration) ([]string, error)
}

type InstanceSource interface {
	PendingPlacement(ctx context.Context) ([]Pending, error)
	AssignedCounts(ctx context.Context) (map[string]int, error)
	Assign(ctx context.Context, instanceID, nodeID string) error
	StrandedOn(ctx context.Context, nodeIDs []string) ([]Stranded, error)
	ReleasePlacement(ctx context.Context, instanceID, nodeID string) error
}

type AddressSource interface {
	Allocate(ctx context.Context, instanceID, networkID, nodeID string) error
}

type Load struct {
	MemoryFreeMiB int
	Fresh         bool
}

type LoadSource interface {
	NodeLoad(ctx context.Context) (map[string]Load, error)
}

type Scheduler struct {
	nodes     NodeSource
	instances InstanceSource
	addresses AddressSource
	loads     LoadSource
	log       *slog.Logger
	interval  time.Duration
	grace     time.Duration
}

func New(
	nodes NodeSource,
	instances InstanceSource,
	addresses AddressSource,
	log *slog.Logger,
	interval time.Duration,
) *Scheduler {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Scheduler{
		nodes:     nodes,
		instances: instances,
		addresses: addresses,
		log:       log,
		interval:  interval,
		grace:     DefaultStrandedGrace,
	}
}

func (s *Scheduler) UseLoad(loads LoadSource) {
	s.loads = loads
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.log.Info("scheduler started", "interval", s.interval.String())

	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				s.log.Warn("scheduling pass failed", "error", err)
			}
		}
	}
}

func (s *Scheduler) Tick(ctx context.Context) error {
	if err := s.releaseStranded(ctx); err != nil {
		return err
	}

	pending, err := s.instances.PendingPlacement(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	candidates, err := s.nodes.ReadyNodes(ctx)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		s.log.Warn("instances are waiting but no node is ready", "waiting", len(pending))
		return nil
	}

	load := map[string]Load{}
	if s.loads != nil {
		measured, err := s.loads.NodeLoad(ctx)
		if err != nil {
			s.log.Warn("could not read node load, placing by instance count", "error", err)
		} else {
			load = measured
		}
	}

	counts, err := s.instances.AssignedCounts(ctx)
	if err != nil {
		return err
	}

	for _, p := range pending {
		target := leastLoaded(candidates, counts, load)
		if err := s.instances.Assign(ctx, p.ID, target.ID); err != nil {
			s.log.Warn("assignment failed", "instance", p.ID, "node", target.ID, "error", err)
			continue
		}
		counts[target.ID]++
		s.log.Info("instance placed", "instance", p.ID, "name", p.Name, "node", target.Name)

		if s.addresses == nil || p.NetworkID == "" {
			continue
		}
		if err := s.addresses.Allocate(ctx, p.ID, p.NetworkID, target.ID); err != nil {
			s.log.Warn("address allocation failed",
				"instance", p.ID, "network", p.NetworkID, "node", target.ID, "error", err)
		}
	}

	return nil
}

func (s *Scheduler) releaseStranded(ctx context.Context) error {
	lost, err := s.nodes.UnreachableNodes(ctx, s.grace)
	if err != nil {
		return err
	}
	if len(lost) == 0 {
		return nil
	}

	stranded, err := s.instances.StrandedOn(ctx, lost)
	if err != nil {
		return err
	}

	for _, in := range stranded {
		if in.Isolation != movableIsolation {
			s.log.Warn("leaving a workload on an unreachable node because moving it would lose its disk",
				"instance", in.ID, "name", in.Name, "isolation", in.Isolation, "node", in.NodeID)
			continue
		}

		if err := s.instances.ReleasePlacement(ctx, in.ID, in.NodeID); err != nil {
			s.log.Warn("could not release a stranded workload",
				"instance", in.ID, "node", in.NodeID, "error", err)
			continue
		}
		s.log.Info("released a workload from an unreachable node",
			"instance", in.ID, "name", in.Name, "node", in.NodeID)
	}

	return nil
}

func leastLoaded(candidates []Candidate, counts map[string]int, load map[string]Load) Candidate {
	ordered := make([]Candidate, len(candidates))
	copy(ordered, candidates)

	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := load[ordered[i].ID], load[ordered[j].ID]
		if left.Fresh && right.Fresh && left.MemoryFreeMiB != right.MemoryFreeMiB {
			return left.MemoryFreeMiB > right.MemoryFreeMiB
		}

		ci, cj := counts[ordered[i].ID], counts[ordered[j].ID]
		if ci != cj {
			return ci < cj
		}
		return ordered[i].Name < ordered[j].Name
	})

	return ordered[0]
}
