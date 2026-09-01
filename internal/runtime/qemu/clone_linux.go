//go:build linux

package qemu

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func (r *Runtime) CloneVolume(newID, fromID, snapshot string) error {
	if strings.ContainsAny(newID, "/.") || strings.ContainsAny(fromID, "/.") {
		return fmt.Errorf("refusing to clone %q into %q", fromID, newID)
	}
	if strings.ContainsAny(snapshot, "/.") {
		return fmt.Errorf("refusing a snapshot named %q", snapshot)
	}

	source, err := r.volumeFile(fromID)
	if err != nil {
		return fmt.Errorf("the volume being copied from is not on this node: %w", err)
	}
	if err := os.MkdirAll(r.volumeDir(), 0o750); err != nil {
		return fmt.Errorf("create the volume directory: %w", err)
	}

	partial := r.volumeDir() + "/" + newID + ".part"
	final := r.volumeDir() + "/" + newID + ".qcow2"

	_ = os.Remove(partial)
	out, err := exec.Command("qemu-img", "convert",
		"-O", "qcow2", "-l", "snapshot.name="+snapshot, source, partial).CombinedOutput()
	if err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("copy snapshot %s of %s: %w: %s",
			snapshot, fromID, err, strings.TrimSpace(string(out)))
	}

	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("place the copied volume: %w", err)
	}

	r.log.Info("volume copied from a snapshot",
		"volume", newID, "from", fromID, "snapshot", snapshot)
	return nil
}
