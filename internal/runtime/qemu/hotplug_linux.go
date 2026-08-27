//go:build linux

package qemu

import (
	"fmt"
	"os"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (r *Runtime) SyncDisks(instanceID string, disks []workload.Disk) (int, error) {
	if len(disks) == 0 {
		return 0, nil
	}

	path := r.monitorSocket(instanceID)
	if _, err := os.Stat(path); err != nil {
		return 0, nil
	}

	monitor, err := dialQMP(path)
	if err != nil {
		return 0, err
	}
	defer monitor.close()

	present, err := monitor.attached()
	if err != nil {
		return 0, err
	}

	plugged := 0
	for _, disk := range disks {
		id := diskDeviceID(disk.ID)

		file, err := r.ensureVolume(disk)
		if err != nil {
			return plugged, err
		}
		if present[id] || present[file] {
			continue
		}

		if err := r.plug(monitor, id, file, disk); err != nil {
			return plugged, err
		}

		r.log.Info("disk attached to a running guest",
			"instance", instanceID, "volume", disk.Name, "device", id)
		plugged++
	}
	return plugged, nil
}

func (r *Runtime) plug(monitor *qmpConn, id, file string, disk workload.Disk) error {
	var err error

	if disk.KeyFile != "" {
		key, readErr := os.ReadFile(disk.KeyFile)
		if readErr != nil {
			return fmt.Errorf("read the volume key: %w", readErr)
		}
		_, err = monitor.run("object-add", map[string]any{
			"qom-type": "secret",
			"id":       id + "key",
			"data":     string(key),
		})
		if err != nil {
			return err
		}
	}

	node := map[string]any{
		"driver":    "qcow2",
		"node-name": id,
		"file": map[string]any{
			"driver":   "file",
			"filename": file,
		},
	}
	if disk.KeyFile != "" {
		node["encrypt"] = map[string]any{
			"format":     "luks",
			"key-secret": id + "key",
		}
	}

	if _, err := monitor.run("blockdev-add", node); err != nil {
		return err
	}

	_, err = monitor.run("device_add", map[string]any{
		"driver": "virtio-blk-pci",
		"id":     id + "dev",
		"drive":  id,
		"serial": disk.Name,
	})
	return err
}
