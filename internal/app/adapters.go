package app

import (
	"context"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/platform/dns"
	"github.com/marstack-labs/marstack-cloud/internal/platform/instance"
	"github.com/marstack-labs/marstack-cloud/internal/platform/network"
	"github.com/marstack-labs/marstack-cloud/internal/platform/node"
	"github.com/marstack-labs/marstack-cloud/internal/platform/scheduler"
)

type nodeSource struct {
	nodes *node.Module
}

func (s nodeSource) UnreachableNodes(ctx context.Context, grace time.Duration) ([]string, error) {
	lost, err := s.nodes.NodesUnreachableFor(ctx, grace)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(lost))
	for _, n := range lost {
		ids = append(ids, n.ID)
	}
	return ids, nil
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
		pending = append(pending, scheduler.Pending{
			ID:        in.ID,
			Name:      in.Name,
			NetworkID: in.NetworkID,
		})
	}
	return pending, nil
}

func (s instanceSource) StrandedOn(ctx context.Context, nodeIDs []string) ([]scheduler.Stranded, error) {
	stranded, err := s.instances.StrandedOn(ctx, nodeIDs)
	if err != nil {
		return nil, err
	}

	out := make([]scheduler.Stranded, 0, len(stranded))
	for _, in := range stranded {
		out = append(out, scheduler.Stranded{
			ID:        in.ID,
			Name:      in.Name,
			NodeID:    in.NodeID,
			Isolation: string(in.Isolation),
		})
	}
	return out, nil
}

func (s instanceSource) ReleasePlacement(ctx context.Context, instanceID, nodeID string) error {
	return s.instances.ReleasePlacement(ctx, instanceID, nodeID)
}

func (s instanceSource) AssignedCounts(ctx context.Context) (map[string]int, error) {
	return s.instances.AssignedCounts(ctx)
}

func (s instanceSource) Assign(ctx context.Context, instanceID, nodeID string) error {
	return s.instances.Assign(ctx, instanceID, nodeID)
}

type addressSource struct {
	networks *network.Module
}

func (s addressSource) Allocate(ctx context.Context, instanceID, networkID, nodeID string) error {
	return s.networks.Allocate(ctx, instanceID, networkID, nodeID)
}

type dnsInstances struct {
	instances *instance.Module
}

func (s dnsInstances) AllInstances(ctx context.Context) ([]dns.InstanceRef, error) {
	all, err := s.instances.All(ctx)
	if err != nil {
		return nil, err
	}

	refs := make([]dns.InstanceRef, 0, len(all))
	for _, in := range all {
		refs = append(refs, dns.InstanceRef{ID: in.ID, Name: in.Name, NetworkID: in.NetworkID})
	}
	return refs, nil
}
