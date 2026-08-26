//go:build linux

package microvm

import (
	"context"
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

	"github.com/marstack-labs/marstack-cloud/internal/runtime/console"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/image"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/netdev"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	stopGrace = 10 * time.Second
	pollEvery = 100 * time.Millisecond
)

type tracked struct {
	cmd      *exec.Cmd
	exited   bool
	exitCode int
}

type Runtime struct {
	root   string
	log    *slog.Logger
	vmm    VMM
	images *image.Store

	mu       sync.Mutex
	running  map[string]*tracked
	consoles map[string]*console.Hub
}

func New(root string, log *slog.Logger, vmm VMM) *Runtime {
	if root == "" {
		root = DefaultRoot
	}
	return &Runtime{
		root:     root,
		log:      log,
		vmm:      vmm,
		images:   image.New(filepath.Join(root, "cache"), log),
		running:  map[string]*tracked{},
		consoles: map[string]*console.Hub{},
	}
}

func (r *Runtime) Name() string {
	return r.vmm.Name()
}

func (r *Runtime) group() string {
	return filepath.Join(r.root, "microvms")
}

func (r *Runtime) instanceDir(instanceID string) string {
	return filepath.Join(r.group(), instanceID)
}

func (r *Runtime) rootfsFile(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "rootfs.ext4")
}

func (r *Runtime) pidFile(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "pid")
}

func (r *Runtime) consoleFile(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), console.LogName)
}

func (r *Runtime) serialSocket(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), console.UpstreamName)
}

func (r *Runtime) openConsole(instanceID string) {
	if !r.vmm.HasSerialSocket() {
		return
	}

	r.mu.Lock()
	_, known := r.consoles[instanceID]
	r.mu.Unlock()
	if known {
		return
	}

	hub, err := console.Attach(r.instanceDir(instanceID), r.log)
	if err != nil {
		r.log.Warn("cannot attach to the microvm console", "instance", instanceID, "error", err)
		return
	}

	r.mu.Lock()
	r.consoles[instanceID] = hub
	r.mu.Unlock()
}

func (r *Runtime) closeConsole(instanceID string) {
	r.mu.Lock()
	hub, known := r.consoles[instanceID]
	delete(r.consoles, instanceID)
	r.mu.Unlock()

	if known {
		hub.Close()
	}
}

func (r *Runtime) launchLog(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "vmm.log")
}

func (r *Runtime) kernel() (string, error) {
	path := filepath.Join(r.root, "images", KernelFileName)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no kernel staged on this node: expected %s", path)
	}
	return path, nil
}

func (r *Runtime) List(context.Context) ([]string, error) {
	entries, err := os.ReadDir(r.group())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list microvms: %w", err)
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
		return errors.New("the microvm runtime must run as root")
	}
	if _, err := exec.LookPath(r.vmm.Binary()); err != nil {
		return fmt.Errorf("%s is not installed on this node", r.vmm.Binary())
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return errors.New("/dev/kvm is not available on this node")
	}

	if state, err := r.Status(ctx, spec.InstanceID); err == nil && state.Phase == workload.PhaseRunning {
		r.openConsole(spec.InstanceID)
		return nil
	}

	kernel, err := r.kernel()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(r.instanceDir(spec.InstanceID), 0o750); err != nil {
		return fmt.Errorf("create instance directory: %w", err)
	}
	if err := r.prepareRootfs(ctx, spec); err != nil {
		return err
	}

	tap, mac, err := r.prepareNetwork(spec)
	if err != nil {
		return err
	}

	socket := filepath.Join(r.instanceDir(spec.InstanceID), "api.sock")
	_ = os.Remove(socket)
	_ = os.Remove(r.serialSocket(spec.InstanceID))

	args, err := r.vmm.Arguments(bootConfig{
		Kernel:     kernel,
		Cmdline:    cmdline(time.Now(), r.vmm.ConsoleDevice()),
		Rootfs:     r.rootfsFile(spec.InstanceID),
		Tap:        tap,
		MAC:        mac,
		VCPU:       spec.VCPU,
		MemoryMiB:  spec.MemoryMiB,
		SerialSock: r.serialSocket(spec.InstanceID),
		APISocket:  socket,
		ConfigFile: filepath.Join(r.instanceDir(spec.InstanceID), "config.json"),
	})
	if err != nil {
		return err
	}

	launch, err := os.OpenFile(r.launchLog(spec.InstanceID),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open vmm log: %w", err)
	}
	defer launch.Close()

	cmd := exec.Command(r.vmm.Binary(), args...)
	cmd.Stderr = launch
	cmd.Stdout = launch

	if !r.vmm.HasSerialSocket() {
		guest, err := os.OpenFile(r.consoleFile(spec.InstanceID),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open console log: %w", err)
		}
		defer guest.Close()
		cmd.Stdout = guest
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", r.vmm.Binary(), err)
	}

	if err := os.WriteFile(r.pidFile(spec.InstanceID),
		[]byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	entry := &tracked{cmd: cmd}
	r.mu.Lock()
	r.running[spec.InstanceID] = entry
	r.mu.Unlock()

	go r.reap(spec.InstanceID, entry)

	r.openConsole(spec.InstanceID)

	r.log.Info("microvm started",
		"instance", spec.InstanceID,
		"vmm", r.vmm.Name(),
		"pid", cmd.Process.Pid,
		"vcpu", spec.VCPU,
		"memory_mib", spec.MemoryMiB,
		"tap", tap,
	)
	return nil
}

func cmdline(now time.Time, console string) string {
	return strings.Join([]string{
		"console=" + console,
		"reboot=k",
		"panic=1",
		"root=/dev/vda",
		"rw",
		"init=" + initPath,
		epochKey + "=" + strconv.FormatInt(now.Unix(), 10),
	}, " ")
}

func (r *Runtime) prepareNetwork(spec workload.Spec) (string, string, error) {
	if spec.Network == nil {
		return "", "", nil
	}

	if err := netdev.EnsureBridge(spec.Network.Bridge, spec.Network.BridgeAddr); err != nil {
		return "", "", err
	}
	if err := netdev.EnsureEgress(spec.Network.Bridge, spec.Network.BridgeAddr); err != nil {
		return "", "", err
	}

	tap := netdev.TapName(spec.InstanceID)
	if err := netdev.EnsureTap(tap, spec.Network.Bridge); err != nil {
		return "", "", err
	}
	return tap, spec.Network.MAC, nil
}

func (r *Runtime) reap(instanceID string, entry *tracked) {
	waitErr := entry.cmd.Wait()

	r.mu.Lock()
	entry.exited = true
	entry.exitCode = entry.cmd.ProcessState.ExitCode()
	code := entry.exitCode
	r.mu.Unlock()

	r.log.Info("microvm exited",
		"instance", instanceID,
		"vmm", r.vmm.Name(),
		"exit_code", code,
		"error", waitErr,
		"tail", tailOf(r.launchLog(instanceID)),
	)
}

func (r *Runtime) snapshot(instanceID string) (tracked, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, known := r.running[instanceID]
	if !known {
		return tracked{}, false
	}
	return *entry, true
}

func (r *Runtime) Status(_ context.Context, instanceID string) (workload.State, error) {
	if _, err := os.Stat(r.instanceDir(instanceID)); os.IsNotExist(err) {
		return workload.State{Phase: workload.PhaseAbsent}, nil
	}

	if entry, known := r.snapshot(instanceID); known {
		if !entry.exited {
			return workload.State{Phase: workload.PhaseRunning}, nil
		}
		message := fmt.Sprintf("the microvm exited with code %d", entry.exitCode)
		if tail := tailOf(r.launchLog(instanceID)); tail != "" {
			message += ": " + tail
		}
		return workload.State{
			Phase:    workload.PhaseExited,
			Message:  message,
			ExitCode: entry.exitCode,
		}, nil
	}

	if _, alive := r.livePID(instanceID); alive {
		return workload.State{
			Phase:   workload.PhaseRunning,
			Message: "adopted after an agent restart",
		}, nil
	}

	message := "the microvm is not running"
	if tail := tailOf(r.launchLog(instanceID)); tail != "" {
		message += ": " + tail
	}
	return workload.State{Phase: workload.PhaseExited, Message: message, ExitCode: 1}, nil
}

func (r *Runtime) livePID(instanceID string) (int, bool) {
	raw, err := os.ReadFile(r.pidFile(instanceID))
	if err != nil {
		return 0, false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return 0, false
	}
	return pid, true
}

func (r *Runtime) Stop(_ context.Context, instanceID string) error {
	defer r.closeConsole(instanceID)

	pid, alive := r.livePID(instanceID)
	if !alive {
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal the microvm: %w", err)
	}

	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if _, alive := r.livePID(instanceID); !alive {
			return nil
		}
		time.Sleep(pollEvery)
	}

	r.log.Warn("the microvm did not stop in time, killing", "instance", instanceID, "pid", pid)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill the microvm: %w", err)
	}
	return nil
}

func (r *Runtime) Remove(ctx context.Context, instanceID string) error {
	if err := r.Stop(ctx, instanceID); err != nil {
		return err
	}

	r.mu.Lock()
	delete(r.running, instanceID)
	r.mu.Unlock()

	if err := netdev.DeleteLink(netdev.TapName(instanceID)); err != nil {
		return err
	}
	if err := os.RemoveAll(r.instanceDir(instanceID)); err != nil {
		return fmt.Errorf("remove instance directory: %w", err)
	}
	return nil
}

func tailOf(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	text := strings.TrimSpace(string(raw))
	if len(text) > 200 {
		text = text[len(text)-200:]
	}
	return strings.ReplaceAll(text, "\n", " ")
}
