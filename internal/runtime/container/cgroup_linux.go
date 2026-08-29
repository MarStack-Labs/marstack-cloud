//go:build linux

package container

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const cpuPeriodMicros = 100000

func ensureCgroupV2() error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(cgroupRoot, &st); err != nil {
		return fmt.Errorf("stat %s: %w", cgroupRoot, err)
	}
	if st.Type != unixCgroup2Magic {
		return errors.New("cgroup v2 is not mounted at " + cgroupRoot)
	}
	return nil
}

func enableControllers(dir string) error {
	path := filepath.Join(dir, "cgroup.subtree_control")
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	available := map[string]bool{}
	for _, c := range strings.Fields(string(current)) {
		available[c] = true
	}

	var missing []string
	for _, c := range []string{"cpu", "memory", "pids"} {
		if !available[c] {
			missing = append(missing, "+"+c)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	if err := os.WriteFile(path, []byte(strings.Join(missing, " ")), 0o644); err != nil {
		return fmt.Errorf("enable controllers in %s: %w", path, err)
	}
	return nil
}

func createCgroup(id string, vcpu, memoryMiB int, l layout) (string, error) {
	if err := ensureCgroupV2(); err != nil {
		return "", err
	}

	slice := filepath.Join(cgroupRoot, cgroupSlice)
	if err := os.MkdirAll(slice, 0o755); err != nil {
		return "", fmt.Errorf("create cgroup slice: %w", err)
	}
	if err := enableControllers(cgroupRoot); err != nil {
		return "", err
	}
	if err := enableControllers(slice); err != nil {
		return "", err
	}

	dir := l.cgroup(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cgroup: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte("512"), 0o644); err != nil {
		return "", fmt.Errorf("write pids.max: %w", err)
	}
	if err := applyLimits(dir, vcpu, memoryMiB); err != nil {
		return "", err
	}

	return dir, nil
}

func removeCgroup(dir string) error {
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cgroup: %w", err)
	}
	return nil
}

func cgroupHasProcesses(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return false
	}
	return len(strings.Fields(string(raw))) > 0
}

func processBelongsToCgroup(pid int, id string) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), filepath.Join(cgroupSlice, id))
}

func applyLimits(dir string, vcpu, memoryMiB int) error {
	limits := map[string]string{
		"memory.max": strconv.Itoa(memoryMiB * 1024 * 1024),
		"cpu.max":    fmt.Sprintf("%d %d", vcpu*cpuPeriodMicros, cpuPeriodMicros),
	}
	for file, value := range limits {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", file, err)
		}
	}
	return nil
}
