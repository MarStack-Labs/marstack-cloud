//go:build linux

package qemu

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	releaseWait  = 15 * time.Second
	releaseCheck = 500 * time.Millisecond
)

func (r *Runtime) ReleaseDisks(
	instanceID string, keep, known []workload.Disk,
) ([]workload.Released, error) {
	path := r.monitorSocket(instanceID)
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}

	monitor, err := dialQMP(path)
	if err != nil {
		return nil, err
	}
	defer monitor.close()

	wanted := make(map[string]bool, len(keep))
	for _, disk := range keep {
		wanted[diskDeviceID(disk.ID)] = true
	}

	released := make([]workload.Released, 0, 2)
	for _, disk := range known {
		device := diskDeviceID(disk.ID)
		if wanted[device] {
			continue
		}

		outcome := workload.Released{VolumeID: disk.ID}
		if err := r.unplug(monitor, device); err != nil {
			outcome.Reason = err.Error()
		} else {
			outcome.Gone = true
			r.log.Info("disk released by a running guest",
				"instance", instanceID, "volume", disk.Name, "device", device)
		}
		released = append(released, outcome)
	}
	return released, nil
}

func missing(err error) bool {
	text := err.Error()
	return strings.Contains(text, "not found") || strings.Contains(text, "does not exist")
}

func (r *Runtime) unplug(monitor *qmpConn, device string) error {
	if _, err := monitor.run("device_del",
		map[string]any{"id": device + "dev"}); err != nil && !missing(err) {
		return fmt.Errorf("ask the guest to release the disk: %w", err)
	}

	deadline := time.Now().Add(releaseWait)
	for {
		_, err := monitor.run("blockdev-del", map[string]any{"node-name": device})
		if err == nil || missing(err) {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"the guest still holds %s after %s. A guest cannot be made to release a "+
					"filesystem it is writing to: unmount it inside the guest and this "+
					"finishes on its own, or stop the guest to take the disk now",
				device, releaseWait)
		}
		time.Sleep(releaseCheck)
	}

	if _, err := monitor.run("object-del",
		map[string]any{"id": device + "key"}); err != nil && !missing(err) {
		return fmt.Errorf(
			"the disk is out but its key object is not: %w. Attaching this volume again is "+
				"refused as a duplicate until the guest restarts", err)
	}
	return nil
}
