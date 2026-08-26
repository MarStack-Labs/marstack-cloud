package agent

import (
	"context"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	DefaultFenceAfter = 60 * time.Second

	fenceIsolation = "container"
)

func (a *Agent) noteContact() {
	a.contactMu.Lock()
	defer a.contactMu.Unlock()
	a.lastContact = a.now()
}

func (a *Agent) silentFor() (time.Duration, bool) {
	a.contactMu.Lock()
	defer a.contactMu.Unlock()

	if a.lastContact.IsZero() {
		return 0, false
	}
	return a.now().Sub(a.lastContact), true
}

func (a *Agent) fenced() (time.Duration, bool) {
	silence, spoken := a.silentFor()
	if !spoken {
		return 0, true
	}
	return silence, silence >= a.cfg.FenceAfter
}

func (a *Agent) fence(ctx context.Context, silence time.Duration) {
	reason := "silent for " + silence.Round(time.Second).String()
	if silence == 0 {
		reason = "never reached since this agent started"
	}

	a.log.Error("fencing this node: the control plane has been unreachable long enough that it "+
		"may hand this work to somebody else",
		"reason", reason,
		"fence_after", a.cfg.FenceAfter,
	)

	runtime, known := a.runtimeFor(fenceIsolation)
	if !known {
		return
	}

	present, err := runtime.List(ctx)
	if err != nil {
		a.log.Error("could not list local workloads while fencing", "error", err)
		return
	}

	for _, id := range present {
		observed, err := runtime.Status(ctx, id)
		if err != nil || observed.Phase != workload.PhaseRunning {
			continue
		}

		a.log.Warn("stopping a movable workload before it can run twice",
			"instance", id, "isolation", fenceIsolation)

		if err := runtime.Stop(ctx, id); err != nil {
			a.log.Error("could not stop a workload while fencing", "instance", id, "error", err)
			continue
		}
		a.noteFenced(id, silence)
	}
}

func (a *Agent) noteFenced(instanceID string, silence time.Duration) {
	a.fencedMu.Lock()
	defer a.fencedMu.Unlock()
	a.fencedFor[instanceID] = silence
}

func (a *Agent) takeFenced(instanceID string) (time.Duration, bool) {
	a.fencedMu.Lock()
	defer a.fencedMu.Unlock()

	silence, found := a.fencedFor[instanceID]
	if found {
		delete(a.fencedFor, instanceID)
	}
	return silence, found
}
