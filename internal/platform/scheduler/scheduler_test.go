package scheduler

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

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
	assignments []assignment
	released    []string
	pendingErr  error
	assignErr   error
	releaseErr  error
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
