package agent

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

type clock struct {
	at time.Time
}

func (c *clock) now() time.Time {
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.at = c.at.Add(d)
}

func exitedInstance(policy string, observed string) instanceView {
	in := runningInstance()
	in.RestartPolicy = policy
	in.ObservedState = observed
	return in
}

func newRestartHarness(t *testing.T, in instanceView, exitCode int) (*Agent, *controlPlane, *fakeRuntime, *clock) {
	t.Helper()

	cp := &controlPlane{
		instances: []instanceView{in},
		networks:  []networkView{defaultNetworkView(in.ID, "10.20.0.65")},
	}
	rt := &fakeRuntime{state: workload.State{
		Phase:    workload.PhaseExited,
		Message:  "exited with code " + itoa(exitCode),
		ExitCode: exitCode,
	}}

	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtimes: runtimesFor(rt), Datapath: &fakeDatapath{}}, logging.New("error", io.Discard))

	tick := &clock{at: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
	a.now = tick.now

	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	return a, cp, rt, tick
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func TestAlwaysRestartsAWorkloadThatExitedCleanly(t *testing.T) {
	a, cp, rt, _ := newRestartHarness(t, exitedInstance(restartAlways, "running"), 0)

	a.reconcile(context.Background())

	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want the workload restarted", len(rt.started))
	}
	if len(rt.removals()) != 1 {
		t.Fatal("the exited workload was not cleared before restarting")
	}

	report := cp.lastReport(t)
	if report.State != observedRunning {
		t.Fatalf("reported %q, want %q", report.State, observedRunning)
	}
	if report.Restarts != 1 {
		t.Fatalf("restarts = %d, want 1 so a crash loop is visible", report.Restarts)
	}
}

func TestNeverLeavesAnExitedWorkloadAlone(t *testing.T) {
	a, cp, rt, _ := newRestartHarness(t, exitedInstance(restartNever, "running"), 1)

	a.reconcile(context.Background())

	if len(rt.started) != 0 {
		t.Fatal("a workload with restart policy never was restarted")
	}
	if report := cp.lastReport(t); report.State != observedFailed {
		t.Fatalf("reported %q, want %q", report.State, observedFailed)
	}
}

func TestOnFailureIgnoresACleanExit(t *testing.T) {
	a, cp, rt, _ := newRestartHarness(t, exitedInstance(restartOnFailure, "running"), 0)

	a.reconcile(context.Background())

	if len(rt.started) != 0 {
		t.Fatal("a clean exit was restarted under on-failure")
	}
	if report := cp.lastReport(t); report.State != observedStopped {
		t.Fatalf("reported %q, want %q: work that finished successfully is not a failure",
			report.State, observedStopped)
	}
}

func TestNeverReportsACleanExitAsStopped(t *testing.T) {
	a, cp, _, _ := newRestartHarness(t, exitedInstance(restartNever, "running"), 0)

	a.reconcile(context.Background())

	if report := cp.lastReport(t); report.State != observedStopped {
		t.Fatalf("reported %q, want %q for a one-shot workload that completed",
			report.State, observedStopped)
	}
}

func TestOnFailureRestartsANonZeroExit(t *testing.T) {
	a, _, rt, _ := newRestartHarness(t, exitedInstance(restartOnFailure, "running"), 137)

	a.reconcile(context.Background())

	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want a failed exit to be restarted", len(rt.started))
	}
}

func TestRestartsAreSpacedByBackoff(t *testing.T) {
	in := exitedInstance(restartAlways, "running")
	a, cp, rt, tick := newRestartHarness(t, in, 1)

	a.reconcile(context.Background())
	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want the first restart to happen immediately", len(rt.started))
	}

	rt.mu.Lock()
	rt.state = workload.State{Phase: workload.PhaseExited, Message: "exited with code 1", ExitCode: 1}
	rt.mu.Unlock()

	a.reconcile(context.Background())
	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want the second restart held back by backoff", len(rt.started))
	}

	report := cp.lastReport(t)
	if !strings.Contains(report.Message, "restarting in") {
		t.Fatalf("message = %q, want it to say how long the wait is", report.Message)
	}
	if report.State != observedFailed {
		t.Fatalf("reported %q, want a workload waiting to restart to look unhealthy", report.State)
	}

	tick.advance(2 * time.Second)
	a.reconcile(context.Background())

	if len(rt.started) != 2 {
		t.Fatalf("starts = %d, want the restart once the backoff elapsed", len(rt.started))
	}
}

func TestAStartFailureKeepsExplainingItself(t *testing.T) {
	a, cp, rt, tick := newRestartHarness(t, exitedInstance(restartAlways, "running"), 1)

	reason := "disk image alpine:3.20 is not present on this node"
	rt.mu.Lock()
	rt.startErr = errors.New(reason)
	rt.mu.Unlock()

	a.reconcile(context.Background())

	if report := cp.lastReport(t); !strings.Contains(report.Message, reason) {
		t.Fatalf("first message = %q, want the reason the start failed", report.Message)
	}

	rt.mu.Lock()
	rt.state = workload.State{Phase: workload.PhaseExited, Message: "the vm is not running", ExitCode: 1}
	rt.mu.Unlock()

	tick.advance(2 * time.Second)
	a.reconcile(context.Background())

	report := cp.lastReport(t)
	if !strings.Contains(report.Message, reason) {
		t.Fatalf("later message = %q, want the reason to survive instead of being replaced "+
			"by the generic exit message", report.Message)
	}
	if report.State != observedFailed {
		t.Fatalf("reported %q, want %q", report.State, observedFailed)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if got := backoffFor(1); got != time.Second {
		t.Fatalf("first backoff = %s, want 1s", got)
	}
	if got := backoffFor(3); got != 4*time.Second {
		t.Fatalf("third backoff = %s, want 4s", got)
	}
	if got := backoffFor(30); got != maxRestartBackoff {
		t.Fatalf("thirtieth backoff = %s, want it capped at %s", got, maxRestartBackoff)
	}
}

func TestRestartCounterResetsAfterTheWorkloadStaysUp(t *testing.T) {
	a, _, rt, tick := newRestartHarness(t, exitedInstance(restartAlways, "running"), 1)

	a.reconcile(context.Background())
	if a.restartAttempts("i-1") != 1 {
		t.Fatalf("attempts = %d, want 1", a.restartAttempts("i-1"))
	}

	tick.advance(stableFor + time.Second)

	rt.mu.Lock()
	rt.state = workload.State{Phase: workload.PhaseExited, Message: "exited with code 1", ExitCode: 1}
	rt.mu.Unlock()

	a.reconcile(context.Background())

	if got := a.restartAttempts("i-1"); got != 1 {
		t.Fatalf("attempts = %d, want the counter reset after a stable run", got)
	}
}

func TestStoppingAWorkloadForgetsItsRestarts(t *testing.T) {
	a, _, _, _ := newRestartHarness(t, exitedInstance(restartAlways, "running"), 1)

	a.reconcile(context.Background())
	if a.restartAttempts("i-1") == 0 {
		t.Fatal("no restart was recorded")
	}

	rt, _ := a.runtimeFor("container")
	a.ensureStopped(context.Background(), rt, "i-1", workload.State{Phase: workload.PhaseExited})

	if got := a.restartAttempts("i-1"); got != 0 {
		t.Fatalf("attempts = %d, want a deliberate stop to clear the history", got)
	}
}

func TestStartingAfterAStopIsNotCountedAsARestart(t *testing.T) {
	a, cp, rt, _ := newRestartHarness(t, exitedInstance(restartAlways, "running"), 0)

	runtime, _ := a.runtimeFor("container")
	a.ensureStopped(context.Background(), runtime, "i-1", workload.State{Phase: workload.PhaseRunning})

	a.reconcile(context.Background())

	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want the workload started again", len(rt.started))
	}
	if got := a.restartAttempts("i-1"); got != 0 {
		t.Fatalf("attempts = %d, want 0: a stop followed by a start is not a crash", got)
	}
	if len(cp.reports) != 0 {
		t.Fatalf("reports = %d, want none: nothing about the instance changed", len(cp.reports))
	}
}
