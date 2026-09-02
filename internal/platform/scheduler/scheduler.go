package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
)

const (
	DefaultInterval = 5 * time.Second

	DefaultStrandedGrace = 2 * time.Minute

	movableIsolation = "container"
)

type Candidate struct {
	ID     string
	Name   string
	Zone   string
	Labels map[string]string
}

func (c Candidate) matches(selector map[string]string) bool {
	for key, want := range selector {
		if c.Labels[key] != want {
			return false
		}
	}
	return true
}

type Pending struct {
	ID        string
	ProjectID string
	Name      string
	NetworkID string
	Group     string
	Strict    bool
	Selector  map[string]string
	Extra     []string
}

type Stranded struct {
	ID         string
	ProjectID  string
	Name       string
	NodeID     string
	Isolation  string
	Migrating  bool
	DiskParked bool
}

type NodeSource interface {
	ReadyNodes(ctx context.Context) ([]Candidate, error)
	UnreachableNodes(ctx context.Context, grace time.Duration) ([]string, error)
	DrainingNodes(ctx context.Context) ([]string, error)
	FinishDraining(ctx context.Context, nodeID string) error
}

type InstanceSource interface {
	PendingPlacement(ctx context.Context) ([]Pending, error)
	AssignedCounts(ctx context.Context) (map[string]int, error)
	GroupCounts(ctx context.Context, group string) (map[string]int, error)
	HoldPlacement(ctx context.Context, instanceID, reason string) error
	Assign(ctx context.Context, instanceID, nodeID string) error
	StrandedOn(ctx context.Context, nodeIDs []string) ([]Stranded, error)
	BeginMigration(ctx context.Context, instanceID, nodeID string) error
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
	events    events.Recorder
	log       *slog.Logger
	interval  time.Duration
	grace     time.Duration

	strandedMu    sync.Mutex
	stranded      map[string]bool
	stuckDraining map[string]bool
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

func (s *Scheduler) UseEvents(recorder events.Recorder) {
	s.events = recorder
}

func (s *Scheduler) note(ctx context.Context, entry events.Entry) {
	if s.events == nil {
		return
	}
	s.events.Record(ctx, entry)
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
	if err := s.emptyDraining(ctx); err != nil {
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

	groups := map[string]map[string]int{}

	for _, p := range pending {
		members, err := s.membersOf(ctx, groups, p.Group)
		if err != nil {
			return err
		}

		eligible := matching(candidates, p.Selector)
		if len(eligible) == 0 {
			s.holdUnmatched(ctx, p)
			continue
		}

		target, room := bestFor(eligible, counts, load, members)
		if p.Strict && p.Group != "" && !room {
			s.hold(ctx, p)
			continue
		}

		if err := s.instances.Assign(ctx, p.ID, target.ID); err != nil {
			s.log.Warn("assignment failed", "instance", p.ID, "node", target.ID, "error", err)
			continue
		}
		counts[target.ID]++
		if p.Group != "" {
			members[target.ID]++
		}
		s.log.Info("instance placed",
			"instance", p.ID, "name", p.Name, "node", target.Name, "group", p.Group)
		s.note(ctx, events.Entry{
			ProjectID: p.ProjectID,
			Kind:      "instance.placed",
			Subject:   p.ID,
			NodeID:    target.ID,
			Message:   "scheduled onto " + target.Name,
			Severity:  events.Info,
		})

		if s.addresses == nil || p.NetworkID == "" {
			continue
		}
		for _, networkID := range append([]string{p.NetworkID}, p.Extra...) {
			if err := s.addresses.Allocate(ctx, p.ID, networkID, target.ID); err != nil {
				s.log.Warn("address allocation failed",
					"instance", p.ID, "network", networkID, "node", target.ID, "error", err)
			}
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
		s.forgetRecovered(nil)
		return nil
	}

	stranded, err := s.instances.StrandedOn(ctx, lost)
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(stranded))
	for _, in := range stranded {
		seen[in.ID] = true

		if in.Isolation != movableIsolation {
			s.log.Warn("leaving a workload on an unreachable node because moving it would lose its disk",
				"instance", in.ID, "name", in.Name, "isolation", in.Isolation, "node", in.NodeID)
			if s.alreadyStranded(in.ID) {
				continue
			}
			s.note(ctx, events.Entry{
				ProjectID: in.ProjectID,
				Kind:      "instance.stranded",
				Subject:   in.ID,
				NodeID:    in.NodeID,
				Message: "its node stopped answering, and isolation " + in.Isolation +
					" cannot be moved without losing its disk",
				Severity: events.Error,
			})
			continue
		}

		if err := s.instances.ReleasePlacement(ctx, in.ID, in.NodeID); err != nil {
			s.log.Warn("could not release a stranded workload",
				"instance", in.ID, "node", in.NodeID, "error", err)
			continue
		}
		s.log.Info("released a workload from an unreachable node",
			"instance", in.ID, "name", in.Name, "node", in.NodeID)
		s.note(ctx, events.Entry{
			ProjectID: in.ProjectID,
			Kind:      "instance.rescheduled",
			Subject:   in.ID,
			NodeID:    in.NodeID,
			Message:   "released from a node that stopped answering, waiting for a new one",
			Severity:  events.Warn,
		})
	}

	s.forgetRecovered(seen)
	return nil
}

func (s *Scheduler) emptyDraining(ctx context.Context) error {
	draining, err := s.nodes.DrainingNodes(ctx)
	if err != nil {
		return err
	}
	if len(draining) == 0 {
		s.forgetUnstuck(nil)
		return nil
	}

	held, err := s.instances.StrandedOn(ctx, draining)
	if err != nil {
		return err
	}

	stuck := map[string]int{}
	seen := map[string]bool{}
	for _, in := range held {
		seen[in.ID] = true

		if in.Isolation != movableIsolation {
			if !carriesItsDisk[in.Isolation] {
				stuck[in.NodeID]++
				s.noteBlocked(ctx, in)
				continue
			}
			if s.carryDisk(ctx, in) {
				stuck[in.NodeID]++
				continue
			}
		}

		if err := s.instances.ReleasePlacement(ctx, in.ID, in.NodeID); err != nil {
			s.log.Warn("could not move a workload off a draining node",
				"instance", in.ID, "node", in.NodeID, "error", err)
			stuck[in.NodeID]++
			continue
		}
		s.log.Info("moved a workload off a draining node",
			"instance", in.ID, "name", in.Name, "node", in.NodeID)
		s.note(ctx, events.Entry{
			ProjectID: in.ProjectID,
			Kind:      "instance.moved",
			Subject:   in.ID,
			NodeID:    in.NodeID,
			Message:   "released from a draining node, waiting for a new one",
			Severity:  events.Info,
		})
	}

	s.forgetUnstuck(seen)

	for _, nodeID := range draining {
		if stuck[nodeID] > 0 {
			continue
		}
		if err := s.nodes.FinishDraining(ctx, nodeID); err != nil {
			s.log.Warn("could not mark a drain finished", "node", nodeID, "error", err)
			continue
		}
		s.log.Info("node drained", "node", nodeID)
		s.note(ctx, events.Entry{
			Kind:     "node.drained",
			Subject:  nodeID,
			NodeID:   nodeID,
			Message:  "nothing movable is left on it, and it stays unschedulable",
			Severity: events.Info,
		})
	}
	return nil
}

var carriesItsDisk = map[string]bool{
	"vm":      true,
	"microvm": true,
	"sandbox": true,
}

func (s *Scheduler) noteBlocked(ctx context.Context, in Stranded) {
	if s.alreadyStuckDraining(in.ID) {
		return
	}
	s.note(ctx, events.Entry{
		ProjectID: in.ProjectID,
		Kind:      "instance.drain_blocked",
		Subject:   in.ID,
		NodeID:    in.NodeID,
		Message: "its node is draining, and nothing here knows how to carry the disk of " +
			"isolation " + in.Isolation,
		Severity: events.Error,
	})
}

func (s *Scheduler) carryDisk(ctx context.Context, in Stranded) bool {
	if in.DiskParked {
		return false
	}

	if err := s.instances.BeginMigration(ctx, in.ID, in.NodeID); err != nil {
		s.log.Warn("could not start moving a workload off a draining node",
			"instance", in.ID, "node", in.NodeID, "error", err)
		return true
	}
	if in.Migrating {
		return true
	}

	s.note(ctx, events.Entry{
		ProjectID: in.ProjectID,
		Kind:      "instance.migrating",
		Subject:   in.ID,
		NodeID:    in.NodeID,
		Message: "its node is draining, so it is being stopped and its disk carried to " +
			"another node",
		Severity: events.Warn,
	})
	return true
}

func (s *Scheduler) forgetUnstuck(seen map[string]bool) {
	s.strandedMu.Lock()
	defer s.strandedMu.Unlock()

	for id := range s.stuckDraining {
		if !seen[id] {
			delete(s.stuckDraining, id)
		}
	}
}

func (s *Scheduler) alreadyStuckDraining(instanceID string) bool {
	s.strandedMu.Lock()
	defer s.strandedMu.Unlock()

	if s.stuckDraining == nil {
		s.stuckDraining = map[string]bool{}
	}
	if s.stuckDraining[instanceID] {
		return true
	}
	s.stuckDraining[instanceID] = true
	return false
}

func (s *Scheduler) alreadyStranded(instanceID string) bool {
	s.strandedMu.Lock()
	defer s.strandedMu.Unlock()

	if s.stranded == nil {
		s.stranded = map[string]bool{}
	}
	if s.stranded[instanceID] {
		return true
	}
	s.stranded[instanceID] = true
	return false
}

func (s *Scheduler) forgetRecovered(seen map[string]bool) {
	s.strandedMu.Lock()
	defer s.strandedMu.Unlock()

	for id := range s.stranded {
		if !seen[id] {
			delete(s.stranded, id)
		}
	}
}

func bestFor(
	candidates []Candidate, counts map[string]int, load map[string]Load, members map[string]int,
) (Candidate, bool) {
	byZone := map[string]int{}
	for _, c := range candidates {
		byZone[c.Zone] += members[c.ID]
	}

	ordered := make([]Candidate, len(candidates))
	copy(ordered, candidates)

	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]

		if zi, zj := byZone[left.Zone], byZone[right.Zone]; zi != zj {
			return zi < zj
		}
		if mi, mj := members[left.ID], members[right.ID]; mi != mj {
			return mi < mj
		}

		li, lj := load[left.ID], load[right.ID]
		if li.Fresh && lj.Fresh && li.MemoryFreeMiB != lj.MemoryFreeMiB {
			return li.MemoryFreeMiB > lj.MemoryFreeMiB
		}
		if ci, cj := counts[left.ID], counts[right.ID]; ci != cj {
			return ci < cj
		}
		return left.Name < right.Name
	})

	return ordered[0], members[ordered[0].ID] == 0
}

func (s *Scheduler) membersOf(
	ctx context.Context, cache map[string]map[string]int, group string,
) (map[string]int, error) {
	if group == "" {
		return map[string]int{}, nil
	}
	if known, ok := cache[group]; ok {
		return known, nil
	}

	members, err := s.instances.GroupCounts(ctx, group)
	if err != nil {
		return nil, err
	}
	cache[group] = members
	return members, nil
}

func matching(candidates []Candidate, selector map[string]string) []Candidate {
	if len(selector) == 0 {
		return candidates
	}

	eligible := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.matches(selector) {
			eligible = append(eligible, c)
		}
	}
	return eligible
}

func describe(selector map[string]string) string {
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+selector[key])
	}
	return strings.Join(pairs, " ")
}

func (s *Scheduler) holdUnmatched(ctx context.Context, p Pending) {
	reason := "no ready node carries " + describe(p.Selector)

	s.log.Info("placement held",
		"instance", p.ID, "name", p.Name, "selector", describe(p.Selector))
	if err := s.instances.HoldPlacement(ctx, p.ID, reason); err != nil {
		s.log.Warn("could not record why a placement was held",
			"instance", p.ID, "error", err)
	}
}

func (s *Scheduler) hold(ctx context.Context, p Pending) {
	reason := "every ready node already runs a member of placement group " + p.Group

	s.log.Info("placement held", "instance", p.ID, "name", p.Name, "group", p.Group)
	if err := s.instances.HoldPlacement(ctx, p.ID, reason); err != nil {
		s.log.Warn("could not record why a placement was held",
			"instance", p.ID, "error", err)
	}
}
