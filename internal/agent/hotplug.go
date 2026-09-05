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

func (a *Agent) releaseDisks(
	ctx context.Context,
	in instanceView,
	keep []workload.Disk,
	volumes []volumeView,
	report bool,
) {
	if !report || in.DesiredState != desiredRunning {
		return
	}

	runtime, known := a.runtimeFor(in.Isolation)
	if !known {
		return
	}

	releaser, able := runtime.(workload.DiskReleaser)
	if !able {
		return
	}

	wanted := false
	for _, v := range volumes {
		if v.InstanceID == in.ID && v.Detaching {
			wanted = true
			break
		}
	}
	if !wanted {
		return
	}

	state, err := runtime.Status(ctx, in.ID)
	if err != nil || state.Phase != workload.PhaseRunning {
		return
	}

	released, err := releaser.ReleaseDisks(in.ID, keep, everyDisk(in.ID, volumes))
	if err != nil {
		a.log.Warn("could not ask a guest to release a disk",
			"instance", in.ID, "error", err)
		return
	}
	if len(released) == 0 {
		return
	}

	reports := make([]reportedVolumeBody, 0, len(released))
	for _, one := range released {
		if !one.Gone {
			a.log.Warn("a guest will not release a disk",
				"instance", in.ID, "volume", one.VolumeID, "reason", one.Reason)
			reports = append(reports,
				reportedVolumeBody{VolumeID: one.VolumeID, Error: one.Reason})
			continue
		}
		reports = append(reports,
			reportedVolumeBody{VolumeID: one.VolumeID, Detached: true})
	}

	if len(reports) == 0 {
		return
	}
	if err := a.client.reportVolumes(ctx, a.currentNodeID(), reports); err != nil {
		a.log.Warn("could not report a released disk", "instance", in.ID, "error", err)
	}
}

func everyDisk(instanceID string, volumes []volumeView) []workload.Disk {
	held := make([]workload.Disk, 0, 2)
	for _, v := range volumes {
		if v.InstanceID != instanceID {
			continue
		}
		held = append(held, workload.Disk{ID: v.ID, Name: v.Name, SizeGiB: v.SizeGiB})
	}
	return held
}
