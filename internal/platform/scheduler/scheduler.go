package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

const DefaultInterval = 5 * time.Second

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

type NodeSource interface {
	ReadyNodes(ctx context.Context) ([]Candidate, error)
}

type InstanceSource interface {
	PendingPlacement(ctx context.Context) ([]Pending, error)
	AssignedCounts(ctx context.Context) (map[string]int, error)
	Assign(ctx context.Context, instanceID, nodeID string) error
}

type AddressSource interface {
	Allocate(ctx context.Context, instanceID, networkID, nodeID string) error
}

type Scheduler struct {
	nodes     NodeSource
	instances InstanceSource
	addresses AddressSource
	log       *slog.Logger
	interval  time.Duration
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
	}
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

	counts, err := s.instances.AssignedCounts(ctx)
	if err != nil {
		return err
	}

	for _, p := range pending {
		target := leastLoaded(candidates, counts)
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

func leastLoaded(candidates []Candidate, counts map[string]int) Candidate {
	ordered := make([]Candidate, len(candidates))
	copy(ordered, candidates)

	sort.SliceStable(ordered, func(i, j int) bool {
		ci, cj := counts[ordered[i].ID], counts[ordered[j].ID]
		if ci != cj {
			return ci < cj
		}
		return ordered[i].Name < ordered[j].Name
	})

	return ordered[0]
}
