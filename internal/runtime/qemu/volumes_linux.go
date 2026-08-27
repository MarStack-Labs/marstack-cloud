//go:build linux

package qemu

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (r *Runtime) PruneVolumes(keep []string) error {
	entries, err := os.ReadDir(r.volumeDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}

	wanted := make(map[string]bool, len(keep))
	for _, id := range keep {
		wanted[id] = true
	}

	root, err := os.OpenRoot(r.volumeDir())
	if err != nil {
		return fmt.Errorf("open the volume directory: %w", err)
	}
	defer root.Close()

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".qcow2") {
			continue
		}
		if wanted[strings.TrimSuffix(name, ".qcow2")] {
			continue
		}

		r.log.Info("removing a volume the control plane no longer knows",
			"file", filepath.Join(r.volumeDir(), name))
		if err := root.Remove(name); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return nil
}

func (r *Runtime) GrowVolume(plan workload.GrowPlan) error {
	path, err := r.volumeFile(plan.VolumeID)
	if err != nil {
		return nil
	}

	current, err := virtualSize(path, plan.KeyFile)
	if err != nil {
		return err
	}

	wanted := int64(plan.SizeGiB) << 30
	if current >= wanted {
		return nil
	}

	args := append([]string{"resize"}, imageArgs(path, plan.KeyFile)...)
	args = append(args, strconv.FormatInt(wanted, 10))

	if out, err := exec.Command("qemu-img", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("grow volume %s: %w: %s",
			plan.VolumeID, err, strings.TrimSpace(string(out)))
	}

	r.log.Info("volume grown",
		"volume", plan.VolumeID, "from_bytes", current, "to_gib", plan.SizeGiB)
	return nil
}
