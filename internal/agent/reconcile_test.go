package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

type fakeRuntime struct {
	mu       sync.Mutex
	state    workload.State
	started  []workload.Spec
	stopped  []string
	startErr error
	stopErr  error
	statErr  error
}

func (f *fakeRuntime) Name() string { return "fake" }

func (f *fakeRuntime) Start(_ context.Context, spec workload.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, spec)
	f.state = workload.State{Phase: workload.PhaseRunning}
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, instanceID)
	f.state = workload.State{Phase: workload.PhaseExited, Message: "exited with code 0"}
	return nil
}

func (f *fakeRuntime) Status(context.Context, string) (workload.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.statErr != nil {
		return workload.State{}, f.statErr
	}
	return f.state, nil
}

func (f *fakeRuntime) Remove(context.Context, string) error { return nil }

type report struct {
	InstanceID string
	State      string
	Message    string
}

type controlPlane struct {
	mu        sync.Mutex
	instances []instanceView
	reports   []report
}

func (c *controlPlane) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/nodes/register", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc", Name: "bm-1"})
	})
	mux.HandleFunc("POST /v1/nodes/{id}/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc"})
	})
	mux.HandleFunc("GET /v1/nodes/{id}/instances", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		json.NewEncoder(w).Encode(instanceListBody{Instances: c.instances})
	})
	mux.HandleFunc("PUT /v1/nodes/{nodeID}/instances/{instanceID}/status",
		func(w http.ResponseWriter, r *http.Request) {
			var body statusBody
			json.NewDecoder(r.Body).Decode(&body)

			c.mu.Lock()
			c.reports = append(c.reports, report{
				InstanceID: r.PathValue("instanceID"),
				State:      body.ObservedState,
				Message:    body.Message,
			})
			c.mu.Unlock()

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		})

	return mux
}

func (c *controlPlane) lastReport(t *testing.T) report {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.reports) == 0 {
		t.Fatal("the agent reported nothing")
	}
	return c.reports[len(c.reports)-1]
}

func newReconcileHarness(t *testing.T, cp *controlPlane, rt workload.Runtime) *Agent {
	t.Helper()

	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		rt, logging.New("error", io.Discard))

	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	return a
}

func runningInstance() instanceView {
	return instanceView{
		ID:            "i-1",
		Name:          "api-1",
		Image:         "alpine:3.20",
		Command:       []string{"/bin/sh", "-c", "sleep 100"},
		VCPU:          1,
		MemoryMiB:     512,
		DesiredState:  "running",
		ObservedState: "pending",
	}
}

func TestReconcileStartsAWorkloadThatShouldRun(t *testing.T) {
	cp := &controlPlane{instances: []instanceView{runningInstance()}}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want 1", len(rt.started))
	}
	if got := rt.started[0]; got.Image != "alpine:3.20" || len(got.Command) != 3 {
		t.Fatalf("spec = %+v, want the image and command passed through", got)
	}
	if got := cp.lastReport(t); got.State != observedRunning {
		t.Fatalf("reported %q, want %q", got.State, observedRunning)
	}
}

func TestReconcileDoesNotRestartAWorkloadAlreadyRunning(t *testing.T) {
	instance := runningInstance()
	instance.ObservedState = "running"

	cp := &controlPlane{instances: []instanceView{instance}}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 0 {
		t.Fatalf("starts = %d, want 0: the workload is already running", len(rt.started))
	}
	if len(cp.reports) != 0 {
		t.Fatalf("reports = %d, want 0: nothing changed, so nothing to say", len(cp.reports))
	}
}

func TestReconcileStopsAWorkloadThatShouldNotRun(t *testing.T) {
	instance := runningInstance()
	instance.DesiredState = "stopped"
	instance.ObservedState = "running"

	cp := &controlPlane{instances: []instanceView{instance}}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.stopped) != 1 {
		t.Fatalf("stops = %d, want 1", len(rt.stopped))
	}
	if got := cp.lastReport(t); got.State != observedStopped {
		t.Fatalf("reported %q, want %q", got.State, observedStopped)
	}
}

func TestReconcileReportsFailureWhenStartFails(t *testing.T) {
	cp := &controlPlane{instances: []instanceView{runningInstance()}}
	rt := &fakeRuntime{
		state:    workload.State{Phase: workload.PhaseAbsent},
		startErr: errors.New("image alpine:3.20 is not present on this node"),
	}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	got := cp.lastReport(t)
	if got.State != observedFailed {
		t.Fatalf("reported %q, want %q", got.State, observedFailed)
	}
	if got.Message == "" {
		t.Fatal("the failure carried no message; an operator cannot act on that")
	}
}

func TestReconcileReportsFailureWhenAWorkloadDies(t *testing.T) {
	instance := runningInstance()
	instance.ObservedState = "running"

	cp := &controlPlane{instances: []instanceView{instance}}
	rt := &fakeRuntime{state: workload.State{
		Phase:   workload.PhaseExited,
		Message: "exited with code 137",
	}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	got := cp.lastReport(t)
	if got.State != observedFailed {
		t.Fatalf("reported %q, want %q", got.State, observedFailed)
	}
	if got.Message != "exited with code 137" {
		t.Fatalf("message = %q, want the runtime's own explanation", got.Message)
	}
	if len(rt.started) != 0 {
		t.Fatal("the agent restarted a dead workload; restart policy is not implemented yet")
	}
}

func TestReconcileSurvivesAnUninspectableWorkload(t *testing.T) {
	cp := &controlPlane{instances: []instanceView{runningInstance()}}
	rt := &fakeRuntime{statErr: errors.New("cgroup vanished")}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if got := cp.lastReport(t); got.State != observedFailed {
		t.Fatalf("reported %q, want %q", got.State, observedFailed)
	}
}

func TestReconcileIsSkippedWithoutARuntime(t *testing.T) {
	cp := &controlPlane{instances: []instanceView{runningInstance()}}

	a := newReconcileHarness(t, cp, nil)
	a.runtime = nil
	a.reconcile(context.Background())

	if len(cp.reports) != 0 {
		t.Fatalf("reports = %d, want 0: an agent with no runtime must not claim anything", len(cp.reports))
	}
}
