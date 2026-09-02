package agent

import (
	"context"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (a *Agent) carrierFor(isolation string) (workload.DiskCarrier, bool) {
	runtime, known := a.runtimeFor(isolation)
	if !known {
		return nil, false
	}
	carrier, able := runtime.(workload.DiskCarrier)
	return carrier, able
}

func (a *Agent) carryDisks(ctx context.Context, nodeID string, assigned []instanceView) {
	for _, in := range assigned {
		if !in.Migrating {
			continue
		}

		carrier, able := a.carrierFor(in.Isolation)
		if !able {
			continue
		}

		if in.DiskParked {
			a.takeDisk(ctx, nodeID, in, carrier)
			continue
		}
		a.handOverDisk(ctx, nodeID, in, carrier)
	}
}

func (a *Agent) handOverDisk(ctx context.Context, nodeID string, in instanceView,
	carrier workload.DiskCarrier) {
	if !carrier.HasDisk(in.ID) {
		return
	}
	if in.ObservedState != observedStopped && in.ObservedState != observedFailed {
		return
	}

	content, err := carrier.ExportDisk(in.ID)
	if err != nil {
		a.log.Warn("could not read the disk of a workload being moved",
			"instance", in.ID, "error", err)
		return
	}
	defer content.Close()

	if err := a.client.parkDisk(ctx, nodeID, in.ID, content); err != nil {
		a.log.Warn("could not hand over the disk of a workload being moved",
			"instance", in.ID, "error", err)
		return
	}

	if err := carrier.Forget(in.ID); err != nil {
		a.log.Warn("handed over a disk but could not remove the copy left here",
			"instance", in.ID, "error", err)
		return
	}
	a.log.Info("disk handed over for a move", "instance", in.ID, "name", in.Name)
}

func (a *Agent) takeDisk(ctx context.Context, nodeID string, in instanceView,
	carrier workload.DiskCarrier) {
	if carrier.HasDisk(in.ID) {
		if err := a.client.diskLanded(ctx, nodeID, in.ID); err != nil {
			a.log.Warn("took a disk but could not say so",
				"instance", in.ID, "error", err)
		}
		return
	}

	content, err := a.client.takeDisk(ctx, nodeID, in.ID)
	if err != nil {
		a.log.Warn("could not fetch the disk of a workload being moved here",
			"instance", in.ID, "error", err)
		return
	}
	defer content.Close()

	if err := carrier.ImportDisk(in.ID, content); err != nil {
		a.log.Warn("could not write the disk of a workload being moved here",
			"instance", in.ID, "error", err)
		return
	}

	a.log.Info("disk taken for a move", "instance", in.ID, "name", in.Name)
	if err := a.client.diskLanded(ctx, nodeID, in.ID); err != nil {
		a.log.Warn("wrote a disk but could not say so", "instance", in.ID, "error", err)
	}
}
