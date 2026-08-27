package agent

import (
	"context"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (a *Agent) plugDisks(ctx context.Context, in instanceView, disks []workload.Disk) {
	if len(disks) == 0 || in.DesiredState != desiredRunning {
		return
	}

	runtime, known := a.runtimeFor(in.Isolation)
	if !known {
		return
	}

	plugger, able := runtime.(workload.HotPlugger)
	if !able {
		return
	}

	state, err := runtime.Status(ctx, in.ID)
	if err != nil || state.Phase != workload.PhaseRunning {
		return
	}

	plugged, err := plugger.SyncDisks(in.ID, disks)
	if err != nil {
		a.log.Warn("could not attach a disk to a running guest",
			"instance", in.ID, "error", err)
		return
	}
	if plugged > 0 {
		a.log.Info("disks attached without a restart", "instance", in.ID, "count", plugged)
	}
}
