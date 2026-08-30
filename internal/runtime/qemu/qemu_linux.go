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

	"github.com/marstack-labs/marstack-cloud/internal/runtime/catalog"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/console"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/netdev"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	stopGrace = 30 * time.Second
	pollEvery = 250 * time.Millisecond

	defaultMAC = "52:54:00:12:34:56"
)

type Images interface {
	Stage(ctx context.Context, reference, kind string) (string, error)
}

type Runtime struct {
	root   string
	log    *slog.Logger
	images Images

	consoles *console.Keeper
}

func New(root string, log *slog.Logger, images Images) *Runtime {
	if root == "" {
		root = DefaultRoot
	}
	return &Runtime{root: root, log: log, images: images, consoles: console.NewKeeper(log)}
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
	return filepath.Join(r.instanceDir(instanceID), console.LogName)
}

func (r *Runtime) monitorSocket(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), "qmp.sock")
}

func (r *Runtime) serialSocket(instanceID string) string {
	return filepath.Join(r.instanceDir(instanceID), console.UpstreamName)
}

func (r *Runtime) ensureConsole(instanceID string) {
	r.consoles.Ensure(r.instanceDir(instanceID), instanceID)
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
		r.ensureConsole(spec.InstanceID)
		return nil
	}

	base := ""
	if spec.Image != "" {
		staged, err := r.stagedDisk(ctx, spec.Image)
		if err != nil {
			return err
		}
		base = staged
	}

	media := ""
	if spec.ISO != "" {
		attached, err := r.stagedISO(ctx, spec.ISO)
		if err != nil {
			return err
		}
		media = attached
	}

	if err := os.MkdirAll(r.instanceDir(spec.InstanceID), 0o750); err != nil {
		return fmt.Errorf("create instance directory: %w", err)
	}

	if err := r.prepareDisk(spec, base); err != nil {
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

	taps, err := r.prepareNetwork(spec)
	if err != nil {
		return err
	}

	launch, err := os.OpenFile(r.launchLog(spec.InstanceID),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open qemu log: %w", err)
	}
	defer launch.Close()

	volumes, err := r.prepareVolumes(spec)
	if err != nil {
		return err
	}

	args := r.arguments(spec, firmware, vars, seed, media, taps, volumes)
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

	r.ensureConsole(spec.InstanceID)

	r.log.Info("vm started",
		"instance", spec.InstanceID,
		"vcpu", spec.VCPU,
		"memory_mib", spec.MemoryMiB,
		"taps", len(taps),
	)
	return nil
}

func (r *Runtime) volumeDir() string {
	return filepath.Join(r.root, "volumes")
}

func (r *Runtime) prepareVolumes(spec workload.Spec) ([]string, error) {
	paths := make([]string, 0, len(spec.Volumes))
	for _, disk := range spec.Volumes {
		path, err := r.ensureVolume(disk)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (r *Runtime) ensureVolume(disk workload.Disk) (string, error) {
	if strings.ContainsAny(disk.ID, "/.") {
		return "", fmt.Errorf("refusing a volume with id %q", disk.ID)
	}
	if err := os.MkdirAll(r.volumeDir(), 0o750); err != nil {
		return "", fmt.Errorf("create the volume directory: %w", err)
	}

	path := filepath.Join(r.volumeDir(), disk.ID+".qcow2")
	if _, err := os.Stat(path); err == nil {
		return path, restrict(path)
	}

	args := []string{"create"}
	if disk.KeyFile != "" {
		args = append(args, "--object", "secret,id=vkey,file="+disk.KeyFile)
	}
	args = append(args, "-f", "qcow2")
	if disk.KeyFile != "" {
		args = append(args, "-o", "encrypt.format=luks,encrypt.key-secret=vkey")
	}
	args = append(args, path, strconv.Itoa(disk.SizeGiB)+"G")

	if out, err := exec.Command("qemu-img", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("create volume %s: %w: %s",
			disk.Name, err, strings.TrimSpace(string(out)))
	}
	if err := restrict(path); err != nil {
		return "", err
	}

	r.log.Info("volume created",
		"volume", disk.Name, "size_gib", disk.SizeGiB, "encrypted", disk.KeyFile != "")
	return path, nil
}

func (r *Runtime) arguments(
	spec workload.Spec, firmware, vars, seed, media string, taps []string, volumes []string,
) []string {
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
		"-serial", "unix:" + r.serialSocket(spec.InstanceID) + ",server=on,wait=off",
		"-qmp", "unix:" + r.monitorSocket(spec.InstanceID) + ",server=on,wait=off",
		"-pidfile", r.pidFile(spec.InstanceID),
		"-daemonize",
		"-drive", "id=root,if=none,format=qcow2,file=" + r.diskFile(spec.InstanceID),
		"-device", "virtio-blk-pci,drive=root,bootindex=1",
	}

	for port := range hotplugPorts {
		args = append(args, "-device",
			"pcie-root-port,id="+portID(port)+",chassis="+strconv.Itoa(port+1))
	}

	for index, path := range volumes {
		disk := spec.Volumes[index]
		id := diskDeviceID(disk.ID)

		if disk.KeyFile != "" {
			args = append(args, "-object", "secret,id="+id+"key,file="+disk.KeyFile)
		}

		drive := "id=" + id + ",if=none,format=qcow2,file=" + path
		if disk.KeyFile != "" {
			drive += ",encrypt.key-secret=" + id + "key"
		}

		args = append(args,
			"-drive", drive,
			"-device", "virtio-blk-pci,drive="+id+",serial="+disk.Name,
		)
	}

	if media != "" {
		args = append(args,
			"-drive", "id=installer,if=none,format=raw,media=cdrom,readonly=on,file="+media,
			"-device", "virtio-blk-pci,drive=installer,bootindex=0",
		)
	}

	args = append(args,
		"-drive", "id=seed,if=none,format=raw,readonly=on,file="+seed,
		"-device", "virtio-blk-pci,drive=seed",
	)

	for device, tap := range taps {
		id := "net" + strconv.Itoa(device)
		args = append(args,
			"-netdev", "tap,id="+id+",ifname="+tap+",script=no,downscript=no",
			"-device", "virtio-net-pci,netdev="+id+",mac="+macOf(spec, device)+",romfile=",
		)
	}
	return args
}

func (r *Runtime) prepareDisk(spec workload.Spec, base string) error {
	disk := r.diskFile(spec.InstanceID)
	if _, err := os.Stat(disk); err == nil {
		return nil
	}

	if base == "" {
		size := spec.DiskGiB
		if size <= 0 {
			size = 10
		}
		blank := exec.Command("qemu-img", "create", "-f", "qcow2", disk,
			strconv.Itoa(size)+"G")
		if out, err := blank.CombinedOutput(); err != nil {
			return fmt.Errorf("create blank disk: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return restrict(disk)
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
	return restrict(disk)
}

func restrict(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict %s: %w", filepath.Base(path), err)
	}
	return nil
}

func (r *Runtime) stagedDisk(ctx context.Context, reference string) (string, error) {
	return r.staged(ctx, reference, catalog.KindDisk)
}

func (r *Runtime) stagedISO(ctx context.Context, reference string) (string, error) {
	return r.staged(ctx, reference, catalog.KindISO)
}

func (r *Runtime) staged(ctx context.Context, reference, kind string) (string, error) {
	if r.images == nil {
		return "", errors.New("this node has no image catalog, so isolation vm cannot boot")
	}
	return r.images.Stage(ctx, reference, kind)
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

func interfaces(spec workload.Spec) []workload.NetworkConfig {
	if spec.Network == nil {
		return nil
	}
	return append([]workload.NetworkConfig{*spec.Network}, spec.Extra...)
}

func macOf(spec workload.Spec, device int) string {
	nics := interfaces(spec)
	if device >= len(nics) || nics[device].MAC == "" {
		return defaultMAC
	}
	return nics[device].MAC
}

func (r *Runtime) prepareNetwork(spec workload.Spec) ([]string, error) {
	nics := interfaces(spec)
	if len(nics) == 0 {
		return nil, nil
	}

	taps := make([]string, 0, len(nics))
	for device, cfg := range nics {
		if err := netdev.EnsureBridge(cfg.Bridge, cfg.BridgeAddr); err != nil {
			return nil, err
		}
		if err := netdev.EnsureEgress(cfg.Bridge, cfg.BridgeAddr); err != nil {
			return nil, err
		}

		tap := netdev.TapName(spec.InstanceID, device)
		if err := netdev.EnsureTap(tap, cfg.Bridge); err != nil {
			return nil, err
		}
		taps = append(taps, tap)
	}
	return taps, nil
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
		r.ensureConsole(instanceID)
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
	defer r.consoles.Release(instanceID)

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
	for device := range netdev.MaxDevices {
		if err := netdev.DeleteLink(netdev.TapName(instanceID, device)); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(r.instanceDir(instanceID)); err != nil {
		return fmt.Errorf("remove instance directory: %w", err)
	}
	return nil
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
