package agent

import (
	"context"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	desiredRunning = "running"
	desiredStopped = "stopped"

	observedRunning = "running"
	observedStopped = "stopped"
	observedFailed  = "failed"
)

func (a *Agent) reconcile(ctx context.Context) {
	if a.runtime == nil {
		return
	}

	assigned, err := a.client.assignedInstances(ctx, a.nodeID)
	if err != nil {
		a.log.Warn("could not read assigned instances", "error", err)
		return
	}

	for _, in := range assigned {
		observed, message := a.reconcileOne(ctx, in)

		if in.ObservedState == observed && in.ObservedMessage == message {
			continue
		}
		if err := a.client.reportStatus(ctx, a.nodeID, in.ID, observed, message); err != nil {
			a.log.Warn("could not report status", "instance", in.ID, "error", err)
		}
	}
}

func (a *Agent) reconcileOne(ctx context.Context, in instanceView) (string, string) {
	spec := workload.Spec{
		InstanceID: in.ID,
		Name:       in.Name,
		Image:      in.Image,
		Command:    in.Command,
		VCPU:       in.VCPU,
		MemoryMiB:  in.MemoryMiB,
	}

	state, err := a.runtime.Status(ctx, in.ID)
	if err != nil {
		return observedFailed, "could not inspect the workload: " + err.Error()
	}

	switch in.DesiredState {
	case desiredRunning:
		return a.ensureRunning(ctx, spec, state)
	case desiredStopped:
		return a.ensureStopped(ctx, in.ID, state)
	default:
		return observedFailed, "unknown desired state " + in.DesiredState
	}
}

func (a *Agent) ensureRunning(ctx context.Context, spec workload.Spec, state workload.State) (string, string) {
	switch state.Phase {
	case workload.PhaseRunning:
		return observedRunning, state.Message

	case workload.PhaseExited:
		return observedFailed, state.Message

	default:
		if err := a.runtime.Start(ctx, spec); err != nil {
			a.log.Warn("could not start workload", "instance", spec.InstanceID, "error", err)
			return observedFailed, err.Error()
		}

		after, err := a.runtime.Status(ctx, spec.InstanceID)
		if err != nil {
			return observedFailed, "started but could not be inspected: " + err.Error()
		}
		if after.Phase != workload.PhaseRunning {
			return observedFailed, after.Message
		}
		return observedRunning, ""
	}
}

func (a *Agent) ensureStopped(ctx context.Context, instanceID string, state workload.State) (string, string) {
	if state.Phase == workload.PhaseRunning {
		if err := a.runtime.Stop(ctx, instanceID); err != nil {
			a.log.Warn("could not stop workload", "instance", instanceID, "error", err)
			return observedFailed, err.Error()
		}
	}
	return observedStopped, ""
}
