//go:build linux

package container

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (r *Runtime) Sample(instanceID string) (workload.Sample, bool) {
	dir := filepath.Join(cgroupRoot, cgroupSlice, instanceID)

	seconds, ok := cpuSeconds(filepath.Join(dir, "cpu.stat"))
	if !ok {
		return workload.Sample{}, false
	}

	memory := 0
	if raw, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil {
		if bytes, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			memory = int(bytes / (1024 * 1024))
		}
	}

	return workload.Sample{CPUSeconds: seconds, MemoryMiB: memory}, true
}

func cpuSeconds(path string) (float64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}

	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found || key != "usage_usec" {
			continue
		}
		micros, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		return micros / 1e6, true
	}
	return 0, false
}
