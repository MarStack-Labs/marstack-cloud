//go:build linux

package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	stopGrace   = 5 * time.Second
	initCommand = "container-init"
)

type tracked struct {
	cmd      *exec.Cmd
	exited   bool
	exitCode int
	message  string
}

type Runtime struct {
	layout  layout
	log     *slog.Logger
	mu      sync.Mutex
	running map[string]*tracked
}

func New(root string, log *slog.Logger) *Runtime {
	if root == "" {
		root = DefaultRoot
	}
	return &Runtime{
		layout:  layout{root: root},
		log:     log,
		running: map[string]*tracked{},
	}
}

func (r *Runtime) Name() string {
	return "container"
}

func (r *Runtime) Start(_ context.Context, spec workload.Spec) error {
	if os.Geteuid() != 0 {
		return errors.New("the container runtime must run as root")
	}
	if len(spec.Command) == 0 {
		return errors.New("no command was given for this instance")
	}

	if state, err := r.Status(context.Background(), spec.InstanceID); err == nil && state.Phase == workload.PhaseRunning {
		return nil
	}

	if err := r.prepareRootfs(spec); err != nil {
		return err
	}

	cgroupDir, err := createCgroup(spec.InstanceID, spec.VCPU, spec.MemoryMiB, r.layout)
	if err != nil {
		return err
	}

	cgroupFD, err := syscall.Open(cgroupDir, syscall.O_DIRECTORY|syscall.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open cgroup directory: %w", err)
	}
	defer syscall.Close(cgroupFD)

	cfg, err := json.Marshal(initConfig{
		Hostname: spec.Name,
		Rootfs:   r.layout.rootfs(spec.InstanceID),
		Command:  spec.Command,
	})
	if err != nil {
		return fmt.Errorf("encode init config: %w", err)
	}

	output, err := os.OpenFile(r.layout.logFile(spec.InstanceID),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("open output log: %w", err)
	}
	defer output.Close()

	cmd := exec.Command("/proc/self/exe", initCommand)
	cmd.Env = append(os.Environ(), initEnvConfig+"="+string(cfg))
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWNET,
		Unshareflags: syscall.CLONE_NEWNS,
		Setsid:       true,
		UseCgroupFD:  true,
		CgroupFD:     cgroupFD,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	if err := os.WriteFile(r.layout.pidFile(spec.InstanceID),
		[]byte(strconv.Itoa(cmd.Process.Pid)), 0o640); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	entry := &tracked{cmd: cmd}
	r.mu.Lock()
	r.running[spec.InstanceID] = entry
	r.mu.Unlock()

	go r.reap(spec.InstanceID, entry)

	r.log.Info("container started",
		"instance", spec.InstanceID,
		"pid", cmd.Process.Pid,
		"vcpu", spec.VCPU,
		"memory_mib", spec.MemoryMiB,
	)
	return nil
}

func (r *Runtime) reap(instanceID string, entry *tracked) {
	err := entry.cmd.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()

	entry.exited = true
	entry.exitCode = entry.cmd.ProcessState.ExitCode()
	if err != nil {
		entry.message = err.Error()
	}

	r.log.Info("container exited",
		"instance", instanceID,
		"exit_code", entry.exitCode,
		"tail", r.tailOutput(instanceID),
	)
}

func (r *Runtime) prepareRootfs(spec workload.Spec) error {
	rootfs := r.layout.rootfs(spec.InstanceID)

	if entries, err := os.ReadDir(rootfs); err == nil && len(entries) > 0 {
		return nil
	}
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return fmt.Errorf("create rootfs: %w", err)
	}

	archive, err := r.findImage(spec.Image)
	if err != nil {
		return err
	}

	extract := exec.Command("tar", "--numeric-owner", "-C", rootfs, "-xf", archive)
	if out, err := extract.CombinedOutput(); err != nil {
		return fmt.Errorf("extract %s: %w: %s", filepath.Base(archive), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *Runtime) findImage(image string) (string, error) {
	base := filepath.Join(r.layout.images(), imageFileName(image))
	for _, suffix := range []string{".tar", ".tar.gz", ".tgz"} {
		candidate := base + suffix
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("image %s is not present on this node: expected %s.tar", image, base)
}

func (r *Runtime) Stop(_ context.Context, instanceID string) error {
	pid, ok := r.livePID(instanceID)
	if !ok {
		return nil
	}

	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal container: %w", err)
	}

	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if _, alive := r.livePID(instanceID); !alive {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	r.log.Warn("container did not stop in time, killing", "instance", instanceID, "pid", pid)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill container: %w", err)
	}
	return nil
}

func (r *Runtime) Status(_ context.Context, instanceID string) (workload.State, error) {
	r.mu.Lock()
	entry, tracked := r.running[instanceID]
	r.mu.Unlock()

	if tracked {
		if !entry.exited {
			return workload.State{Phase: workload.PhaseRunning}, nil
		}
		message := fmt.Sprintf("exited with code %d", entry.exitCode)
		if tail := r.tailOutput(instanceID); tail != "" {
			message += ": " + tail
		}
		return workload.State{Phase: workload.PhaseExited, Message: message}, nil
	}

	if _, alive := r.livePID(instanceID); alive {
		return workload.State{Phase: workload.PhaseRunning, Message: "adopted after an agent restart"}, nil
	}
	return workload.State{Phase: workload.PhaseAbsent}, nil
}

func (r *Runtime) livePID(instanceID string) (int, bool) {
	raw, err := os.ReadFile(r.layout.pidFile(instanceID))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return 0, false
	}
	if !processBelongsToCgroup(pid, instanceID) {
		return 0, false
	}
	return pid, true
}

func (r *Runtime) tailOutput(instanceID string) string {
	raw, err := os.ReadFile(r.layout.logFile(instanceID))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 200 {
		text = text[len(text)-200:]
	}
	return strings.ReplaceAll(text, "\n", " ")
}

func (r *Runtime) Remove(ctx context.Context, instanceID string) error {
	if err := r.Stop(ctx, instanceID); err != nil {
		return err
	}

	r.mu.Lock()
	delete(r.running, instanceID)
	r.mu.Unlock()

	dir := r.layout.cgroup(instanceID)
	if cgroupHasProcesses(dir) {
		return errors.New("cgroup still has processes")
	}
	if err := removeCgroup(dir); err != nil {
		return err
	}
	if err := os.RemoveAll(r.layout.instance(instanceID)); err != nil {
		return fmt.Errorf("remove instance directory: %w", err)
	}
	return nil
}
