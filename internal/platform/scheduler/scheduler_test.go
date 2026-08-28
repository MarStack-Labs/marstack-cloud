package scheduler

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

type fakeNodes struct {
	ready       []Candidate
	unreachable []string
	err         error
	calls       int
}

func (f *fakeNodes) ReadyNodes(context.Context) ([]Candidate, error) {
	f.calls++
	return f.ready, f.err
}

func (f *fakeNodes) UnreachableNodes(context.Context, time.Duration) ([]string, error) {
	return f.unreachable, nil
}

type assignment struct {
	instanceID string
	nodeID     string
}

type fakeInstances struct {
	pending     []Pending
	stranded    []Stranded
	counts      map[string]int
	groups      map[string]map[string]int
	assignments []assignment
	released    []string
	held        map[string]string
	pendingErr  error
	assignErr   error
	releaseErr  error
}

func (f *fakeInstances) GroupCounts(_ context.Context, group string) (map[string]int, error) {
	counts := map[string]int{}
	for node, count := range f.groups[group] {
		counts[node] = count
	}
	return counts, nil
}

func (f *fakeInstances) HoldPlacement(_ context.Context, instanceID, reason string) error {
	if f.held == nil {
		f.held = map[string]string{}
	}
	f.held[instanceID] = reason
	return nil
}

func (f *fakeInstances) StrandedOn(_ context.Context, nodeIDs []string) ([]Stranded, error) {
	wanted := map[string]bool{}
	for _, id := range nodeIDs {
		wanted[id] = true
	}

	matching := make([]Stranded, 0, len(f.stranded))
	for _, in := range f.stranded {
		if wanted[in.NodeID] {
			matching = append(matching, in)
		}
	}
	return matching, nil
}

func (f *fakeInstances) ReleasePlacement(_ context.Context, instanceID, _ string) error {
	if f.releaseErr != nil {
		return f.releaseErr
	}
	f.released = append(f.released, instanceID)
	return nil
}

func (f *fakeInstances) PendingPlacement(context.Context) ([]Pending, error) {
	return f.pending, f.pendingErr
}

func (f *fakeInstances) AssignedCounts(context.Context) (map[string]int, error) {
	if f.counts == nil {
		f.counts = map[string]int{}
	}
	return f.counts, nil
}

func (f *fakeInstances) Assign(_ context.Context, instanceID, nodeID string) error {
	if f.assignErr != nil {
		return f.assignErr
	}
	f.assignments = append(f.assignments, assignment{instanceID, nodeID})
	return nil
}

type fakeAddresses struct {
	allocated []string
	err       error
}

func (f *fakeAddresses) Allocate(_ context.Context, instanceID, _, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.allocated = append(f.allocated, instanceID)
	return nil
}

func newTestScheduler(nodes NodeSource, instances InstanceSource) *Scheduler {
	return New(nodes, instances, &fakeAddresses{}, logging.New("error", io.Discard), time.Hour)
}

func TestPlacesPendingInstanceOnReadyNode(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1", Name: "bm-1"}}}
	instances := &fakeInstances{pending: []Pending{{ID: "i-1", Name: "api-1", NetworkID: "nw-1"}}}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(instances.assignments) != 1 {
		t.Fatalf("assignments = %d, want 1", len(instances.assignments))
	}
	if got := instances.assignments[0]; got.instanceID != "i-1" || got.nodeID != "n-1" {
		t.Fatalf("assignment = %+v, want i-1 on n-1", got)
	}
}

func TestSpreadsAcrossLeastLoadedNodes(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{
		{ID: "n-1", Name: "bm-1"},
		{ID: "n-2", Name: "bm-2"},
	}}
	instances := &fakeInstances{
		pending: []Pending{{ID: "i-1"}, {ID: "i-2"}, {ID: "i-3"}},
		counts:  map[string]int{"n-1": 2},
	}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	got := map[string]int{}
	for _, a := range instances.assignments {
		got[a.nodeID]++
	}
	if got["n-2"] != 2 || got["n-1"] != 1 {
		t.Fatalf("placements = %v, want n-2 to take 2 and n-1 to take 1", got)
	}
}

func TestDoesNothingWhenNothingIsPending(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1"}}}
	instances := &fakeInstances{}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if nodes.calls != 0 {
		t.Fatalf("node lookups = %d, want 0 when nothing is waiting", nodes.calls)
	}
}

func TestWaitsWhenNoNodeIsReady(t *testing.T) {
	nodes := &fakeNodes{ready: nil}
	instances := &fakeInstances{pending: []Pending{{ID: "i-1"}}}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(instances.assignments) != 0 {
		t.Fatalf("assignments = %d, want none: the instance must stay pending", len(instances.assignments))
	}
}

func TestOneFailedAssignmentDoesNotBlockTheRest(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1"}}}
	instances := &fakeInstances{
		pending:   []Pending{{ID: "i-1"}, {ID: "i-2"}},
		assignErr: errors.New("conflict"),
	}

	s := newTestScheduler(nodes, instances)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick returned %v, want the pass to continue past a single failure", err)
	}
}

func TestPropagatesLookupFailures(t *testing.T) {
	nodes := &fakeNodes{}
	instances := &fakeInstances{pendingErr: errors.New("db down")}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err == nil {
		t.Fatal("expected the error to propagate so the caller can log it")
	}
}

func TestRunStopsWithContext(t *testing.T) {
	nodes := &fakeNodes{}
	instances := &fakeInstances{}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		New(nodes, instances, &fakeAddresses{}, logging.New("error", io.Discard), 10*time.Millisecond).Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestAddressIsAllocatedAfterPlacement(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1", Name: "bm-1"}}}
	instances := &fakeInstances{pending: []Pending{{ID: "i-1", NetworkID: "nw-1"}}}
	addresses := &fakeAddresses{}

	s := New(nodes, instances, addresses, logging.New("error", io.Discard), time.Hour)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(addresses.allocated) != 1 || addresses.allocated[0] != "i-1" {
		t.Fatalf("allocated = %v, want [i-1]", addresses.allocated)
	}
}

func TestPlacementSurvivesAddressAllocationFailure(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1", Name: "bm-1"}}}
	instances := &fakeInstances{pending: []Pending{{ID: "i-1", NetworkID: "nw-1"}}}
	addresses := &fakeAddresses{err: errors.New("network exhausted")}

	s := New(nodes, instances, addresses, logging.New("error", io.Discard), time.Hour)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick returned %v, want the placement to stand", err)
	}

	if len(instances.assignments) != 1 {
		t.Fatal("the instance lost its placement because addressing failed")
	}
}

func TestStrandedContainersAreReleasedForReplacement(t *testing.T) {
	nodes := &fakeNodes{
		ready:       []Candidate{{ID: "n-2", Name: "bm-2"}},
		unreachable: []string{"n-1"},
	}
	instances := &fakeInstances{
		stranded: []Stranded{
			{ID: "i-1", Name: "web", NodeID: "n-1", Isolation: "container"},
			{ID: "i-2", Name: "db", NodeID: "n-1", Isolation: "vm"},
			{ID: "i-3", Name: "other", NodeID: "n-9", Isolation: "container"},
		},
	}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(instances.released) != 1 || instances.released[0] != "i-1" {
		t.Fatalf("released = %v, want only the container on the unreachable node", instances.released)
	}
}

func TestVMsAreNotMovedOffAnUnreachableNode(t *testing.T) {
	nodes := &fakeNodes{
		ready:       []Candidate{{ID: "n-2", Name: "bm-2"}},
		unreachable: []string{"n-1"},
	}
	instances := &fakeInstances{
		stranded: []Stranded{{ID: "i-vm", Name: "db", NodeID: "n-1", Isolation: "vm"}},
	}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(instances.released) != 0 {
		t.Fatal("a vm was moved off its node, which would abandon its local disk")
	}
}

func TestNothingIsReleasedWhileEveryNodeReports(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1", Name: "bm-1"}}}
	instances := &fakeInstances{
		stranded: []Stranded{{ID: "i-1", NodeID: "n-1", Isolation: "container"}},
	}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(instances.released) != 0 {
		t.Fatalf("released = %v, want none: no node is unreachable", instances.released)
	}
}

func TestOneFailedReleaseDoesNotStopTheRest(t *testing.T) {
	nodes := &fakeNodes{unreachable: []string{"n-1"}}
	instances := &fakeInstances{
		stranded: []Stranded{
			{ID: "i-1", NodeID: "n-1", Isolation: "container"},
			{ID: "i-2", NodeID: "n-1", Isolation: "container"},
		},
		releaseErr: errors.New("conflict"),
	}

	if err := newTestScheduler(nodes, instances).Tick(context.Background()); err != nil {
		t.Fatalf("tick returned %v, want the pass to continue", err)
	}
}

func TestPlacementPrefersTheNodeWithMoreFreeMemory(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{
		{ID: "n-busy", Name: "aaa"},
		{ID: "n-free", Name: "zzz"},
	}}
	instances := &fakeInstances{pending: []Pending{{ID: "i-1", Name: "web-1"}}}

	s := New(nodes, instances, &fakeAddresses{}, logging.New("error", io.Discard), time.Hour)
	s.UseLoad(fakeLoad{load: map[string]Load{
		"n-busy": {MemoryFreeMiB: 500, Fresh: true},
		"n-free": {MemoryFreeMiB: 4000, Fresh: true},
	}})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if instances.assignedTo("i-1") != "n-free" {
		t.Fatalf("placed on %q, want the node with more free memory even though it sorts last "+
			"by name and holds the same instance count", instances.assignedTo("i-1"))
	}
}

func TestPlacementIgnoresStaleLoad(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{
		{ID: "n-a", Name: "aaa"},
		{ID: "n-b", Name: "bbb"},
	}}
	instances := &fakeInstances{pending: []Pending{{ID: "i-1", Name: "web-1"}}}

	s := New(nodes, instances, &fakeAddresses{}, logging.New("error", io.Discard), time.Hour)
	s.UseLoad(fakeLoad{load: map[string]Load{
		"n-a": {MemoryFreeMiB: 10, Fresh: false},
		"n-b": {MemoryFreeMiB: 9000, Fresh: false},
	}})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if instances.assignedTo("i-1") != "n-a" {
		t.Fatalf("placed on %q, want the count based order when the numbers are old: a node that "+
			"stopped reporting must not look empty", instances.assignedTo("i-1"))
	}
}

func (f *fakeInstances) assignedTo(instanceID string) string {
	for _, placed := range f.assignments {
		if placed.instanceID == instanceID {
			return placed.nodeID
		}
	}
	return ""
}

type fakeLoad struct {
	load map[string]Load
}

func (f fakeLoad) NodeLoad(context.Context) (map[string]Load, error) {
	return f.load, nil
}

func TestAGroupSpreadsAcrossZonesFirst(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{
		{ID: "n-1", Name: "a", Zone: "rack-a"},
		{ID: "n-2", Name: "b", Zone: "rack-a"},
		{ID: "n-3", Name: "c", Zone: "rack-b"},
	}}
	instances := &fakeInstances{
		pending: []Pending{{ID: "i-1", Name: "web-2", Group: "web"}},
		groups:  map[string]map[string]int{"web": {"n-1": 1}},
	}

	s := newTestScheduler(nodes, instances)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	if len(instances.assignments) != 1 {
		t.Fatalf("assignments = %+v", instances.assignments)
	}
	if got := instances.assignments[0].nodeID; got != "n-3" {
		t.Fatalf("placed on %s, want n-3: the other zone holds no member, and a zone is the "+
			"failure domain a group is spread across", got)
	}
}

func TestAGroupAvoidsANodeThatAlreadyHoldsAMember(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{
		{ID: "n-1", Name: "a", Zone: "rack-a"},
		{ID: "n-2", Name: "b", Zone: "rack-a"},
	}}
	instances := &fakeInstances{
		pending: []Pending{{ID: "i-1", Name: "web-2", Group: "web"}},
		groups:  map[string]map[string]int{"web": {"n-1": 1}},
	}

	s := newTestScheduler(nodes, instances)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if got := instances.assignments[0].nodeID; got != "n-2" {
		t.Fatalf("placed on %s, want n-2", got)
	}
}

func TestTwoMembersPlacedInOnePassDoNotShareANode(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{
		{ID: "n-1", Name: "a", Zone: "rack-a"},
		{ID: "n-2", Name: "b", Zone: "rack-b"},
	}}
	instances := &fakeInstances{pending: []Pending{
		{ID: "i-1", Name: "web-1", Group: "web"},
		{ID: "i-2", Name: "web-2", Group: "web"},
	}}

	s := newTestScheduler(nodes, instances)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	if len(instances.assignments) != 2 {
		t.Fatalf("assignments = %+v", instances.assignments)
	}
	if instances.assignments[0].nodeID == instances.assignments[1].nodeID {
		t.Fatal("both members landed on one node, so the scheduler forgot what it had just " +
			"placed within the same pass")
	}
}

func TestStrictPlacementHoldsRatherThanDoubleUp(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1", Name: "a", Zone: "rack-a"}}}
	instances := &fakeInstances{
		pending: []Pending{{ID: "i-2", Name: "web-2", Group: "web", Strict: true}},
		groups:  map[string]map[string]int{"web": {"n-1": 1}},
	}

	s := newTestScheduler(nodes, instances)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	if len(instances.assignments) != 0 {
		t.Fatalf("assigned %+v, want the guarantee kept", instances.assignments)
	}
	if reason := instances.held["i-2"]; reason == "" {
		t.Fatal("nothing was recorded, so the instance sits pending with no explanation")
	} else if !strings.Contains(reason, "web") {
		t.Fatalf("reason = %q, want it to name the group", reason)
	}
}

func TestWithoutStrictAGroupDoublesUpRatherThanStall(t *testing.T) {
	nodes := &fakeNodes{ready: []Candidate{{ID: "n-1", Name: "a", Zone: "rack-a"}}}
	instances := &fakeInstances{
		pending: []Pending{{ID: "i-2", Name: "web-2", Group: "web"}},
		groups:  map[string]map[string]int{"web": {"n-1": 1}},
	}

	s := newTestScheduler(nodes, instances)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(instances.assignments) != 1 {
		t.Fatalf("assignments = %+v, want it placed anyway", instances.assignments)
	}
}

type collector struct {
	entries []events.Entry
}

func (c *collector) Record(_ context.Context, entry events.Entry) {
	c.entries = append(c.entries, entry)
}

func (c *collector) of(kind string) []events.Entry {
	matching := []events.Entry{}
	for _, entry := range c.entries {
		if entry.Kind == kind {
			matching = append(matching, entry)
		}
	}
	return matching
}

func TestAStrandedWorkloadIsRecordedOnceRatherThanEveryTick(t *testing.T) {
	nodes := &fakeNodes{unreachable: []string{"n-dead"}}
	instances := &fakeInstances{stranded: []Stranded{
		{ID: "i-1", ProjectID: "prj-default", Name: "db", NodeID: "n-dead", Isolation: "vm"},
	}}

	seen := &collector{}
	s := newTestScheduler(nodes, instances)
	s.UseEvents(seen)

	for range 5 {
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}

	stranded := seen.of("instance.stranded")
	if len(stranded) != 1 {
		t.Fatalf("events = %d, want one rather than one per pass while the node stays dead",
			len(stranded))
	}
	if stranded[0].Severity != events.Error {
		t.Fatalf("severity = %q, want error: nothing will move it", stranded[0].Severity)
	}
	if stranded[0].ProjectID != "prj-default" {
		t.Fatalf("project = %q, want it set or the event is invisible", stranded[0].ProjectID)
	}
}

func TestAStrandedWorkloadIsRecordedAgainAfterItsNodeRecovers(t *testing.T) {
	nodes := &fakeNodes{unreachable: []string{"n-dead"}}
	instances := &fakeInstances{stranded: []Stranded{
		{ID: "i-1", ProjectID: "prj-default", Name: "db", NodeID: "n-dead", Isolation: "vm"},
	}}

	seen := &collector{}
	s := newTestScheduler(nodes, instances)
	s.UseEvents(seen)

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	nodes.unreachable = nil
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	nodes.unreachable = []string{"n-dead"}
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if stranded := seen.of("instance.stranded"); len(stranded) != 2 {
		t.Fatalf("events = %d, want it recorded again after the node came back and died again",
			len(stranded))
	}
}

func TestReleasingAStrandedContainerIsRecorded(t *testing.T) {
	nodes := &fakeNodes{unreachable: []string{"n-dead"}}
	instances := &fakeInstances{stranded: []Stranded{
		{ID: "i-1", ProjectID: "prj-default", Name: "web", NodeID: "n-dead",
			Isolation: "container"},
	}}

	seen := &collector{}
	s := newTestScheduler(nodes, instances)
	s.UseEvents(seen)

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	moved := seen.of("instance.rescheduled")
	if len(moved) != 1 {
		t.Fatalf("events = %+v, want the release recorded", moved)
	}
	if moved[0].NodeID != "n-dead" {
		t.Fatalf("node_id = %q, want the node it was released from", moved[0].NodeID)
	}
}
