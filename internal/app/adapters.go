package app

import (
	"context"

	"github.com/marstack-labs/marstack-cloud/internal/platform/instance"
	"github.com/marstack-labs/marstack-cloud/internal/platform/node"
	"github.com/marstack-labs/marstack-cloud/internal/platform/scheduler"
)

type nodeSource struct {
	nodes *node.Module
}

func (s nodeSource) ReadyNodes(ctx context.Context) ([]scheduler.Candidate, error) {
	ready, err := s.nodes.ReadyNodes(ctx)
	if err != nil {
		return nil, err
	}

	candidates := make([]scheduler.Candidate, 0, len(ready))
	for _, n := range ready {
		candidates = append(candidates, scheduler.Candidate{ID: n.ID, Name: n.Name, Zone: n.Zone})
	}
	return candidates, nil
}

type instanceSource struct {
	instances *instance.Module
}

func (s instanceSource) PendingPlacement(ctx context.Context) ([]scheduler.Pending, error) {
	waiting, err := s.instances.PendingPlacement(ctx)
	if err != nil {
		return nil, err
	}

	pending := make([]scheduler.Pending, 0, len(waiting))
	for _, in := range waiting {
		pending = append(pending, scheduler.Pending{ID: in.ID, Name: in.Name})
	}
	return pending, nil
}

func (s instanceSource) AssignedCounts(ctx context.Context) (map[string]int, error) {
	return s.instances.AssignedCounts(ctx)
}

func (s instanceSource) Assign(ctx context.Context, instanceID, nodeID string) error {
	return s.instances.Assign(ctx, instanceID, nodeID)
}
