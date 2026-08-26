//go:build linux

package qemu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
