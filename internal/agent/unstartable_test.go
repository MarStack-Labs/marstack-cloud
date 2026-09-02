package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func newStartHarness(t *testing.T, failure error) (*Agent, *controlPlane, *fakeRuntime, *clock) {
	t.Helper()

	in := runningInstance()
	in.ObservedState = "pending"

	cp := &controlPlane{
		instances: []instanceView{in},
		networks:  []networkView{defaultNetworkView(in.ID, "10.20.0.65")},
	}
	rt := &fakeRuntime{
		state:    workload.State{Phase: workload.PhaseAbsent},
		startErr: failure,
	}

	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtimes: runtimesFor(rt), Datapath: &fakeDatapath{}}, logging.New("error", io.Discard))

	tick := &clock{at: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}
	a.now = tick.now

	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	return a, cp, rt, tick
}

func TestAWorkloadThatCanNeverStartIsTriedOnce(t *testing.T) {
	refusal := fmt.Errorf("%w: the image declares no command and none was given",
		workload.ErrUnstartable)
	a, cp, rt, tick := newStartHarness(t, refusal)

	for range 3 {
		a.reconcile(context.Background())
		tick.advance(10 * time.Second)
	}

	if rt.startAttempts() != 1 {
		t.Fatalf("start was called %d times, want once: no amount of waiting adds a command "+
			"to an image, so retrying is a warning every pass for as long as the node runs",
			rt.startAttempts())
	}

	held := cp.lastReport(t)
	if held.State != observedFailed {
		t.Fatalf("observed = %q, want %q", held.State, observedFailed)
	}
	if !strings.Contains(held.Message, "declares no command") {
		t.Fatalf("message = %q, want the reason kept on every pass, not only the first",
			held.Message)
	}
}

func TestAStartThatMayWorkLaterBacksOff(t *testing.T) {
	a, _, rt, tick := newStartHarness(t, errors.New("the image is not on this node yet"))

	a.reconcile(context.Background())
	if rt.startAttempts() != 1 {
		t.Fatalf("start was called %d times, want once", rt.startAttempts())
	}

	tick.advance(500 * time.Millisecond)
	a.reconcile(context.Background())
	if rt.startAttempts() != 1 {
		t.Fatalf("start was called %d times, want the second attempt held back: a start "+
			"that failed is a restart, and a node that retries every pass forever is the "+
			"crash loop it already knows how to slow down", rt.startAttempts())
	}

	tick.advance(2 * time.Second)
	a.reconcile(context.Background())
	if rt.startAttempts() != 2 {
		t.Fatalf("start was called %d times, want a second attempt once the wait passed: "+
			"backoff that never ends is giving up without saying so", rt.startAttempts())
	}
}

func TestWaitingToTryAgainSaysWhyAndWhen(t *testing.T) {
	a, cp, _, tick := newStartHarness(t, errors.New("the image is not on this node yet"))

	a.reconcile(context.Background())
	tick.advance(100 * time.Millisecond)
	a.reconcile(context.Background())

	held := cp.lastReport(t)
	if !strings.Contains(held.Message, "not on this node yet") ||
		!strings.Contains(held.Message, "trying again in") {
		t.Fatalf("message = %q, want the reason and the wait: a workload that is waiting "+
			"and says nothing looks the same as one nobody is looking after", held.Message)
	}
}

func TestAnInstanceThatLeavesTakesItsRestartStateWithIt(t *testing.T) {
	refusal := fmt.Errorf("%w: nothing to run", workload.ErrUnstartable)
	a, cp, _, _ := newStartHarness(t, refusal)

	a.reconcile(context.Background())
	if _, refused := a.unstartable(runningInstance().ID); !refused {
		t.Fatal("the refusal was not remembered, so it is decided again every pass")
	}

	cp.mu.Lock()
	cp.instances = nil
	cp.mu.Unlock()
	a.reconcile(context.Background())

	a.restartsMu.Lock()
	left := len(a.restarts)
	a.restartsMu.Unlock()

	if left != 0 {
		t.Fatalf("%d restart states left, want none: one entry per instance the node ever "+
			"held is a map that only grows", left)
	}
}
