package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func newCachingAgent(t *testing.T, endpoint, stateDir string, rt workload.Runtime) *Agent {
	t.Helper()

	return New(Config{
		Endpoint:          endpoint,
		Name:              "bm-1",
		StateDir:          stateDir,
		Interval:          time.Hour,
		HeartbeatInterval: time.Hour,
	}, Deps{Runtimes: runtimesFor(rt), Datapath: &fakeDatapath{}},
		logging.New("error", io.Discard))
}

func TestReconcileWritesTheDesiredStateToDisk(t *testing.T) {
	dir := t.TempDir()

	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
		records:   []dnsRecordView{{FQDN: "api-1.default.internal", IP: "10.20.0.65"}},
	}

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	a := newCachingAgent(t, srv.URL, dir, &fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}})
	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}

	a.reconcile(context.Background())

	raw, err := os.ReadFile(filepath.Join(dir, cacheFileName))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}

	var saved cachedState
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("decode cache: %v", err)
	}

	if saved.NodeID != "n-abc" {
		t.Errorf("node id = %q, want the registered one", saved.NodeID)
	}
	if len(saved.Instances) != 1 || saved.Instances[0].ID != "i-1" {
		t.Errorf("instances = %+v, want the assigned one", saved.Instances)
	}
	if len(saved.Networks) != 1 || len(saved.Records) != 1 {
		t.Errorf("saved = %+v, want the network view and the zone", saved)
	}
}

func TestAgentStartsWorkloadsFromCacheWhenTheControlPlaneIsDown(t *testing.T) {
	dir := t.TempDir()

	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}

	live := httptest.NewServer(cp.handler())
	primed := newCachingAgent(t, live.URL, dir,
		&fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}})

	if err := primed.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	primed.reconcile(context.Background())
	live.Close()

	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unreachable.Close()

	rebooted := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}
	a := newCachingAgent(t, unreachable.URL, dir, rebooted)

	a.reconcileFromCache(context.Background())

	if len(rebooted.started) != 1 {
		t.Fatalf("starts = %d, want the workload started from cache", len(rebooted.started))
	}
	if got := rebooted.started[0]; got.Network == nil || got.Network.IP != "10.20.0.65" {
		t.Fatalf("spec = %+v, want the cached address applied", got)
	}
	if a.currentNodeID() != "n-abc" {
		t.Fatalf("node id = %q, want it restored from cache", a.currentNodeID())
	}
}

func TestCacheReplayDoesNotReportToTheControlPlane(t *testing.T) {
	dir := t.TempDir()

	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}

	live := httptest.NewServer(cp.handler())
	primed := newCachingAgent(t, live.URL, dir,
		&fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}})
	primed.register(context.Background())
	primed.reconcile(context.Background())
	live.Close()

	var calls int
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer counting.Close()

	a := newCachingAgent(t, counting.URL, dir,
		&fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}})
	a.reconcileFromCache(context.Background())

	if calls != 0 {
		t.Fatalf("control plane calls = %d, want 0: a replay must not talk to a dead control plane", calls)
	}
}

func TestNoCacheMeansNothingIsStarted(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unreachable.Close()

	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}
	a := newCachingAgent(t, unreachable.URL, t.TempDir(), rt)

	a.reconcileFromCache(context.Background())

	if len(rt.started) != 0 {
		t.Fatalf("starts = %d, want none: an agent with no cache must not invent workloads", len(rt.started))
	}
}
