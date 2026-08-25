//go:build linux

package qemu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/runtime/netdev"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	stopGrace = 30 * time.Second
	pollEvery = 250 * time.Millisecond

	tapPrefix = "mst-"
	maxIfName = 15
)

type Runtime struct {
	root string
	log  *slog.Logger
}

func New(root string, log *slog.Logger) *Runtime {
	if root == "" {
		root = DefaultRoot
	}
	return &Runtime{root: root, log: log}
}

func (r *Runtime) Name() string {
	return "vm"
}

func (r *Runtime) instanceDir(instanceID string) string {
	return filepath.Join(r.root, "vms", instanceID)
}

func (r *Runtime) pidFile(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "pid")
}

func (r *Runtime) consoleFile(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "console.log")
}

func (r *Runtime) launchLog(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "qemu.log")
}

func (r *Runtime) diskFile(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "disk.qcow2")
}

func (r *Runtime) List(context.Context) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, "vms"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list vms: %w", err)
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
		return errors.New("the vm runtime must run as root")
	}
	if err := requireTools(); err != nil {
		return err
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return errors.New("/dev/kvm is not available on this node, so a vm cannot be accelerated")
	}

	if state, err := r.Status(ctx, spec.InstanceID); err == nil && state.Phase == workload.PhaseRunning {
		return nil
	}

	if err := os.MkdirAll(r.instanceDir(spec.InstanceID), 0o750); err != nil {
		return fmt.Errorf("create instance directory: %w", err)
	}

	if err := r.prepareDisk(spec); err != nil {
		return err
	}

	firmware, vars, err := r.prepareFirmware(spec.InstanceID)
	if err != nil {
		return err
	}

	seed, err := r.writeSeed(spec)
	if err != nil {
		return err
	}

	tap, err := r.prepareNetwork(spec)
	if err != nil {
		return err
	}

	launch, err := os.OpenFile(r.launchLog(spec.InstanceID),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open qemu log: %w", err)
	}
	defer launch.Close()

	args := r.arguments(spec, firmware, vars, seed, tap)
	cmd := exec.Command(qemuBinary(), args...)
	cmd.Stdout = launch
	cmd.Stderr = launch
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("start qemu: %w: %s", err, r.tailOf(r.launchLog(spec.InstanceID)))
	}

	if err := r.waitForPID(spec.InstanceID); err != nil {
		return err
	}

	r.log.Info("vm started",
		"instance", spec.InstanceID,
		"vcpu", spec.VCPU,
		"memory_mib", spec.MemoryMiB,
		"tap", tap,
	)
	return nil
}

func (r *Runtime) arguments(spec workload.Spec, firmware, vars, seed, tap string) []string {
	mac := "52:54:00:12:34:56"
	if spec.Network != nil && spec.Network.MAC != "" {
		mac = spec.Network.MAC
	}

	args := []string{
		"-name", spec.Name,
		"-machine", "virt,accel=kvm",
		"-cpu", "host",
		"-smp", strconv.Itoa(spec.VCPU),
		"-m", strconv.Itoa(spec.MemoryMiB),
		"-display", "none",
		"-monitor", "none",
		"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=" + firmware,
		"-drive", "if=pflash,format=raw,unit=1,file=" + vars,
		"-drive", "if=virtio,format=qcow2,file=" + r.diskFile(spec.InstanceID),
		"-drive", "if=virtio,format=raw,media=cdrom,readonly=on,file=" + seed,
		"-serial", "file:" + r.consoleFile(spec.InstanceID),
		"-pidfile", r.pidFile(spec.InstanceID),
		"-daemonize",
	}

	if tap != "" {
		args = append(args,
			"-netdev", "tap,id=net0,ifname="+tap+",script=no,downscript=no",
			"-device", "virtio-net-pci,netdev=net0,mac="+mac+",romfile=",
		)
	}
	return args
}

func (r *Runtime) prepareDisk(spec workload.Spec) error {
	disk := r.diskFile(spec.InstanceID)
	if _, err := os.Stat(disk); err == nil {
		return nil
	}

	base, err := r.baseImage(spec.Image)
	if err != nil {
		return err
	}

	create := exec.Command("qemu-img", "create",
		"-f", "qcow2",
		"-F", "qcow2",
		"-b", base,
		disk,
	)
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("create overlay disk: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *Runtime) baseImage(reference string) (string, error) {
	name := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(reference)
	path := filepath.Join(r.root, "images", name+".qcow2")

	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("disk image %s is not present on this node: expected %s", reference, path)
	}
	return path, nil
}

func (r *Runtime) prepareFirmware(instanceID string) (string, string, error) {
	code, template, err := firmwarePaths()
	if err != nil {
		return "", "", err
	}

	vars := filepath.Join(r.instanceDir(instanceID), "efivars.fd")
	if _, err := os.Stat(vars); err != nil {
		contents, err := os.ReadFile(template)
		if err != nil {
			return "", "", fmt.Errorf("read firmware variables: %w", err)
		}
		if err := os.WriteFile(vars, contents, 0o640); err != nil {
			return "", "", fmt.Errorf("write firmware variables: %w", err)
		}
	}
	return code, vars, nil
}

func (r *Runtime) prepareNetwork(spec workload.Spec) (string, error) {
	if spec.Network == nil {
		return "", nil
	}

	if err := netdev.EnsureBridge(spec.Network.Bridge, spec.Network.BridgeAddr); err != nil {
		return "", err
	}
	if err := netdev.EnsureEgress(spec.Network.Bridge, spec.Network.BridgeAddr); err != nil {
		return "", err
	}

	tap := TapName(spec.InstanceID)
	if err := netdev.EnsureTap(tap, spec.Network.Bridge); err != nil {
		return "", err
	}
	return tap, nil
}

func (r *Runtime) waitForPID(instanceID string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, alive := r.livePID(instanceID); alive {
			return nil
		}
		time.Sleep(pollEvery)
	}
	return fmt.Errorf("qemu did not report a pid: %s %s",
		r.tailOf(r.launchLog(instanceID)), r.tailOf(r.consoleFile(instanceID)))
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

func (r *Runtime) Status(_ context.Context, instanceID string) (workload.State, error) {
	if _, err := os.Stat(r.instanceDir(instanceID)); os.IsNotExist(err) {
		return workload.State{Phase: workload.PhaseAbsent}, nil
	}

	if _, alive := r.livePID(instanceID); alive {
		return workload.State{Phase: workload.PhaseRunning}, nil
	}

	message := "the vm is not running"
	if tail := r.tailOf(r.consoleFile(instanceID)); tail != "" {
		message += ": " + tail
	}
	return workload.State{Phase: workload.PhaseExited, Message: message, ExitCode: 1}, nil
}

func (r *Runtime) tailOf(path string) string {
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

func (r *Runtime) Stop(_ context.Context, instanceID string) error {
	pid, alive := r.livePID(instanceID)
	if !alive {
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal qemu: %w", err)
	}

	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if _, alive := r.livePID(instanceID); !alive {
			return nil
		}
		time.Sleep(pollEvery)
	}

	r.log.Warn("vm did not stop in time, killing", "instance", instanceID, "pid", pid)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill qemu: %w", err)
	}
	return nil
}

func (r *Runtime) Remove(ctx context.Context, instanceID string) error {
	if err := r.Stop(ctx, instanceID); err != nil {
		return err
	}
	if err := netdev.DeleteLink(TapName(instanceID)); err != nil {
		return err
	}
	if err := os.RemoveAll(r.instanceDir(instanceID)); err != nil {
		return fmt.Errorf("remove instance directory: %w", err)
	}
	return nil
}

func TapName(instanceID string) string {
	suffix := instanceID
	if index := strings.IndexByte(suffix, '-'); index >= 0 {
		suffix = suffix[index+1:]
	}

	room := maxIfName - len(tapPrefix)
	if len(suffix) > room {
		suffix = suffix[:room]
	}
	return tapPrefix + suffix
}

func qemuBinary() string {
	if runtime.GOARCH == "arm64" {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

func requireTools() error {
	for _, tool := range []string{qemuBinary(), "qemu-img", "xorrisofs"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is not installed on this node, which isolation vm needs", tool)
		}
	}
	return nil
}

func firmwarePaths() (string, string, error) {
	candidates := [][2]string{
		{"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/AAVMF/AAVMF_VARS.fd"},
		{"/usr/share/qemu-efi-aarch64/QEMU_EFI.fd", "/usr/share/qemu-efi-aarch64/QEMU_VARS.fd"},
		{"/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"},
	}

	for _, pair := range candidates {
		if _, err := os.Stat(pair[0]); err != nil {
			continue
		}
		if _, err := os.Stat(pair[1]); err != nil {
			continue
		}
		return pair[0], pair[1], nil
	}
	return "", "", errors.New("no UEFI firmware found on this node, which a cloud image needs to boot")
}
