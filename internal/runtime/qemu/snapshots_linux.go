//go:build linux

package qemu

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (r *Runtime) SyncSnapshots(plans []workload.SnapshotPlan) []workload.SnapshotState {
	states := make([]workload.SnapshotState, 0, len(plans))

	for _, plan := range plans {
		state := workload.SnapshotState{VolumeID: plan.VolumeID}

		path, err := r.volumeFile(plan.VolumeID)
		if err != nil {
			states = append(states, state)
			continue
		}

		if err := r.applySnapshots(path, plan); err != nil {
			state.Error = err.Error()
			r.log.Warn("could not apply the snapshots of a volume",
				"volume", plan.VolumeID, "error", err)
		} else if plan.RestoreTo != "" {
			state.Restored = plan.RestoreTo
		}

		present, err := snapshotsIn(path, plan.KeyFile)
		if err != nil {
			state.Error = err.Error()
		}
		state.Present = present

		states = append(states, state)
	}
	return states
}

func (r *Runtime) volumeFile(volumeID string) (string, error) {
	if strings.ContainsAny(volumeID, "/.") {
		return "", fmt.Errorf("refusing a volume with id %q", volumeID)
	}

	path := r.volumeDir() + "/" + volumeID + ".qcow2"
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func (r *Runtime) applySnapshots(path string, plan workload.SnapshotPlan) error {
	present, err := snapshotsIn(path, plan.KeyFile)
	if err != nil {
		return err
	}

	have := make(map[string]bool, len(present))
	for _, file := range present {
		have[file.Name] = true
	}

	want := make(map[string]bool, len(plan.Wanted))
	for _, name := range plan.Wanted {
		want[name] = true

		if have[name] {
			continue
		}
		if err := snapshot(path, plan.KeyFile, "-c", name); err != nil {
			return err
		}
		r.log.Info("snapshot taken", "volume", plan.VolumeID, "snapshot", name)
	}

	for _, file := range present {
		if want[file.Name] {
			continue
		}
		if err := snapshot(path, plan.KeyFile, "-d", file.Name); err != nil {
			return err
		}
		r.log.Info("snapshot removed", "volume", plan.VolumeID, "snapshot", file.Name)
	}

	if plan.RestoreTo != "" {
		if err := snapshot(path, plan.KeyFile, "-a", plan.RestoreTo); err != nil {
			return err
		}
		r.log.Info("volume restored", "volume", plan.VolumeID, "snapshot", plan.RestoreTo)
	}
	return nil
}

func imageArgs(path, keyFile string) []string {
	if keyFile == "" {
		return []string{path}
	}
	return []string{
		"--object", "secret,id=vkey,file=" + keyFile,
		"--image-opts", "driver=qcow2,file.filename=" + path + ",encrypt.key-secret=vkey",
	}
}

func snapshot(path, keyFile, action, name string) error {
	args := append([]string{"snapshot"}, imageArgs(path, keyFile)...)
	args = append(args, action, name)

	out, err := exec.Command("qemu-img", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img snapshot %s %s: %w: %s",
			action, name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func snapshotsIn(path, keyFile string) ([]workload.SnapshotFile, error) {
	args := append([]string{"info", "--output=json"}, imageArgs(path, keyFile)...)

	out, err := exec.Command("qemu-img", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("read the volume: %w", err)
	}

	var info struct {
		Snapshots []struct {
			Name string `json:"name"`
			Size int64  `json:"vm-state-size"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("decode the volume info: %w", err)
	}

	files := make([]workload.SnapshotFile, 0, len(info.Snapshots))
	for _, snap := range info.Snapshots {
		files = append(files, workload.SnapshotFile{Name: snap.Name, Bytes: snap.Size})
	}
	return files, nil
}
