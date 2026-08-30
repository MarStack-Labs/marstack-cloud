//go:build linux

package microvm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCmdlineCarriesTheConsoleDeviceAndHostClock(t *testing.T) {
	boot := time.Unix(1787671277, 0)

	line := cmdline(boot, "ttyS0")

	if !strings.Contains(line, "console=ttyS0") {
		t.Fatalf("the console device is missing from %q", line)
	}
	if !strings.Contains(line, "ms.epoch=1787671277") {
		t.Fatalf("the host clock is missing from %q", line)
	}
	if !strings.Contains(line, "init="+initPath) {
		t.Fatalf("the init path is missing from %q", line)
	}
}

func TestEachVMMDeclaresItsOwnConsole(t *testing.T) {
	cases := map[string]struct {
		vmm    VMM
		device string
		socket bool
	}{
		"cloud hypervisor exposes a pl011 and a serial socket": {
			vmm: CloudHypervisor(), device: "ttyAMA0", socket: true,
		},
		"firecracker exposes an ns16550a and logs through stdout": {
			vmm: Firecracker(), device: "ttyS0", socket: false,
		},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if got := want.vmm.ConsoleDevice(); got != want.device {
				t.Fatalf("console device: got %q, want %q", got, want.device)
			}
			if got := want.vmm.HasSerialSocket(); got != want.socket {
				t.Fatalf("has serial socket: got %v, want %v", got, want.socket)
			}
		})
	}
}

func TestCloudHypervisorDeclaresTheRawImageType(t *testing.T) {
	args, err := CloudHypervisor().Arguments(bootConfig{
		Rootfs: "/tmp/rootfs.ext4", VCPU: 1, MemoryMiB: 512,
	})
	if err != nil {
		t.Fatalf("build arguments: %v", err)
	}

	if !strings.Contains(strings.Join(args, " "), "path=/tmp/rootfs.ext4,image_type=raw") {
		t.Fatalf("the disk is not declared as raw: %v", args)
	}
}

func TestFirecrackerWritesAConfigFile(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")

	args, err := Firecracker().Arguments(bootConfig{
		Kernel: "/k", Cmdline: "console=ttyS0", Rootfs: "/r",
		NICs: []nic{{Tap: "mst-a", MAC: "02:00:00:00:00:01"}},
		VCPU: 2, MemoryMiB: 256, ConfigFile: config,
	})
	if err != nil {
		t.Fatalf("build arguments: %v", err)
	}
	if !strings.Contains(strings.Join(args, " "), config) {
		t.Fatalf("the config file is not passed: %v", args)
	}

	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var document struct {
		BootSource struct {
			BootArgs string `json:"boot_args"`
		} `json:"boot-source"`
		Interfaces []struct {
			HostDevName string `json:"host_dev_name"`
		} `json:"network-interfaces"`
		Machine struct {
			VCPUCount int `json:"vcpu_count"`
		} `json:"machine-config"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode config: %v", err)
	}

	if document.BootSource.BootArgs != "console=ttyS0" {
		t.Fatalf("boot args: got %q", document.BootSource.BootArgs)
	}
	if len(document.Interfaces) != 1 || document.Interfaces[0].HostDevName != "mst-a" {
		t.Fatalf("interfaces: got %+v", document.Interfaces)
	}
	if document.Machine.VCPUCount != 2 {
		t.Fatalf("vcpu count: got %d", document.Machine.VCPUCount)
	}
}
