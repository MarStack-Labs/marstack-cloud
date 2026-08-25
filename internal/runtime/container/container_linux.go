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

	"github.com/marstack-labs/marstack-cloud/internal/runtime/image"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/netdev"
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
	images  *image.Store
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
		images:  image.New(filepath.Join(root, "cache"), log),
		running: map[string]*tracked{},
	}
}

func (r *Runtime) Name() string {
	return "container"
}

func (r *Runtime) List(context.Context) ([]string, error) {
	dir := filepath.Join(r.layout.root, "instances")

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}

	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}

func (r *Runtime) Start(ctx context.Context, spec workload.Spec) error {
	if os.Geteuid() != 0 {
		return errors.New("the container runtime must run as root")
	}
	if state, err := r.Status(ctx, spec.InstanceID); err == nil && state.Phase == workload.PhaseRunning {
		return nil
	}

	imageConfig, err := r.prepareRootfs(ctx, spec)
	if err != nil {
		return err
	}

	command := spec.Command
	if len(command) == 0 {
		command = imageConfig.Command()
	}
	if len(command) == 0 {
		return errors.New("the image declares no command and none was given")
	}

	if err := r.prepareNetwork(spec); err != nil {
		return err
	}

	if err := r.writeResolvConf(spec); err != nil {
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
		Hostname:   spec.Name,
		Rootfs:     r.layout.rootfs(spec.InstanceID),
		Command:    command,
		Env:        imageConfig.Env,
		WorkingDir: imageConfig.WorkingDir,
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

	gate, release, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create start gate: %w", err)
	}
	defer gate.Close()
	defer release.Close()

	cmd := exec.Command("/proc/self/exe", initCommand)
	cmd.Env = append(os.Environ(), initEnvConfig+"="+string(cfg))
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.ExtraFiles = []*os.File{gate}
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

	if err := r.attachNetwork(spec, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	if _, err := release.Write([]byte{1}); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("release the container: %w", err)
	}

	entry := &tracked{cmd: cmd}
	r.mu.Lock()
	r.running[spec.InstanceID] = entry
	r.mu.Unlock()

	go r.reap(spec.InstanceID, entry)

	r.log.Info("container started",
		"instance", spec.InstanceID,
		"pid", cmd.Process.Pid,
		"command", command,
		"vcpu", spec.VCPU,
		"memory_mib", spec.MemoryMiB,
	)
	return nil
}

func (r *Runtime) writeResolvConf(spec workload.Spec) error {
	if spec.Network == nil || spec.Network.Nameserver == "" {
		return nil
	}

	dir := filepath.Join(r.layout.rootfs(spec.InstanceID), "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create /etc in the rootfs: %w", err)
	}

	contents := "nameserver " + spec.Network.Nameserver + "\n"
	if spec.Network.SearchDomain != "" {
		contents += "search " + spec.Network.SearchDomain + "\noptions ndots:1\n"
	}

	path := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}
	return nil
}

func (r *Runtime) prepareNetwork(spec workload.Spec) error {
	if spec.Network == nil {
		return nil
	}
	if err := netdev.EnsureBridge(spec.Network.Bridge, spec.Network.BridgeAddr); err != nil {
		return err
	}
	return netdev.EnsureEgress(spec.Network.Bridge, spec.Network.BridgeAddr)
}

func (r *Runtime) attachNetwork(spec workload.Spec, pid int) error {
	if spec.Network == nil {
		return nil
	}

	if err := netdev.Attach(pid, netdev.Interface{
		Bridge:     spec.Network.Bridge,
		BridgeAddr: spec.Network.BridgeAddr,
		InstanceID: spec.InstanceID,
		IP:         spec.Network.IP,
		Prefix:     spec.Network.Prefix,
		Gateway:    spec.Network.Gateway,
		MAC:        spec.Network.MAC,
	}); err != nil {
		return fmt.Errorf("attach network: %w", err)
	}

	r.log.Info("interface attached",
		"instance", spec.InstanceID,
		"ip", spec.Network.IP,
		"bridge", spec.Network.Bridge,
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

func (r *Runtime) prepareRootfs(ctx context.Context, spec workload.Spec) (image.Config, error) {
	rootfs := r.layout.rootfs(spec.InstanceID)

	if entries, err := os.ReadDir(rootfs); err == nil && len(entries) > 0 {
		return r.readImageConfig(spec.InstanceID)
	}
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return image.Config{}, fmt.Errorf("create rootfs: %w", err)
	}

	if archive, found := r.findLocalArchive(spec.Image); found {
		extract := exec.Command("tar", "--numeric-owner", "-C", rootfs, "-xf", archive)
		if out, err := extract.CombinedOutput(); err != nil {
			return image.Config{}, fmt.Errorf("extract %s: %w: %s",
				filepath.Base(archive), err, strings.TrimSpace(string(out)))
		}
		return image.Config{}, nil
	}

	config, err := r.images.Pull(ctx, spec.Image, rootfs)
	if err != nil {
		if removeErr := os.RemoveAll(rootfs); removeErr != nil {
			r.log.Warn("could not clean a half-extracted rootfs",
				"instance", spec.InstanceID, "error", removeErr)
		}
		return image.Config{}, err
	}

	if err := r.writeImageConfig(spec.InstanceID, config); err != nil {
		return image.Config{}, err
	}
	return config, nil
}

func (r *Runtime) imageConfigPath(instanceID string) string {
	return filepath.Join(r.layout.instance(instanceID), "image.json")
}

func (r *Runtime) writeImageConfig(instanceID string, config image.Config) error {
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode image config: %w", err)
	}
	if err := os.WriteFile(r.imageConfigPath(instanceID), encoded, 0o640); err != nil {
		return fmt.Errorf("write image config: %w", err)
	}
	return nil
}

func (r *Runtime) readImageConfig(instanceID string) (image.Config, error) {
	raw, err := os.ReadFile(r.imageConfigPath(instanceID))
	if os.IsNotExist(err) {
		return image.Config{}, nil
	}
	if err != nil {
		return image.Config{}, fmt.Errorf("read image config: %w", err)
	}

	var config image.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return image.Config{}, fmt.Errorf("decode image config: %w", err)
	}
	return config, nil
}

func (r *Runtime) findLocalArchive(reference string) (string, bool) {
	base := filepath.Join(r.layout.images(), imageFileName(reference))
	for _, suffix := range []string{".tar", ".tar.gz", ".tgz"} {
		candidate := base + suffix
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
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
		return workload.State{
			Phase:    workload.PhaseExited,
			Message:  message,
			ExitCode: entry.exitCode,
		}, nil
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

	if err := netdev.Detach(instanceID); err != nil {
		return err
	}

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
