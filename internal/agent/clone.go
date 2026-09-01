package agent

import (
	"context"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (a *Agent) cloners() []workload.VolumeCloner {
	found := make([]workload.VolumeCloner, 0, len(a.runtimes))
	for _, runtime := range a.runtimes {
		if cloner, able := runtime.(workload.VolumeCloner); able {
			found = append(found, cloner)
		}
	}
	return found
}

func (a *Agent) cloneVolumes(ctx context.Context, volumes []volumeView,
	assigned []instanceView) {
	cloners := a.cloners()
	if len(cloners) == 0 {
		return
	}

	for _, v := range volumes {
		if v.CloneFrom == "" || v.CloneSnap == "" {
			continue
		}
		if a.volumeIsBusy(ctx, v, assigned) {
			continue
		}

		for _, cloner := range cloners {
			if cloner.HasVolume(v.ID) {
				continue
			}
			if !cloner.HasVolume(v.CloneFrom) {
				continue
			}
			if err := cloner.CloneVolume(v.ID, v.CloneFrom, v.CloneSnap); err != nil {
				a.log.Warn("could not copy a volume from a snapshot",
					"volume", v.ID, "from", v.CloneFrom, "snapshot", v.CloneSnap,
					"error", err)
			}
		}
	}
}
