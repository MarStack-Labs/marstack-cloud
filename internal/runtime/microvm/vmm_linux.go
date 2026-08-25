//go:build linux

package microvm

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type bootConfig struct {
	Kernel     string
	Cmdline    string
	Rootfs     string
	Tap        string
	MAC        string
	VCPU       int
	MemoryMiB  int
	ConsoleLog string
	APISocket  string
	ConfigFile string
}

type VMM interface {
	Name() string
	Binary() string
	Arguments(cfg bootConfig) ([]string, error)
}

type cloudHypervisor struct{}

func CloudHypervisor() VMM {
	return cloudHypervisor{}
}

func (cloudHypervisor) Name() string {
	return "cloud-hypervisor"
}

func (cloudHypervisor) Binary() string {
	return "cloud-hypervisor"
}

func (cloudHypervisor) Arguments(cfg bootConfig) ([]string, error) {
	args := []string{
		"--api-socket", cfg.APISocket,
		"--kernel", cfg.Kernel,
		"--cmdline", cfg.Cmdline,
		"--disk", "path=" + cfg.Rootfs,
		"--cpus", "boot=" + strconv.Itoa(cfg.VCPU),
		"--memory", "size=" + strconv.Itoa(cfg.MemoryMiB) + "M",
		"--serial", "file=" + cfg.ConsoleLog,
		"--console", "off",
	}
	if cfg.Tap != "" {
		args = append(args, "--net", "tap="+cfg.Tap+",mac="+cfg.MAC)
	}
	return args, nil
}

type firecracker struct{}

func Firecracker() VMM {
	return firecracker{}
}

func (firecracker) Name() string {
	return "firecracker"
}

func (firecracker) Binary() string {
	return "firecracker"
}

func (firecracker) Arguments(cfg bootConfig) ([]string, error) {
	type bootSource struct {
		KernelImagePath string `json:"kernel_image_path"`
		BootArgs        string `json:"boot_args"`
	}
	type drive struct {
		DriveID      string `json:"drive_id"`
		PathOnHost   string `json:"path_on_host"`
		IsRootDevice bool   `json:"is_root_device"`
		IsReadOnly   bool   `json:"is_read_only"`
	}
	type iface struct {
		IfaceID     string `json:"iface_id"`
		HostDevName string `json:"host_dev_name"`
		GuestMAC    string `json:"guest_mac"`
	}
	type machine struct {
		VCPUCount  int `json:"vcpu_count"`
		MemSizeMiB int `json:"mem_size_mib"`
	}
	type document struct {
		BootSource bootSource `json:"boot-source"`
		Drives     []drive    `json:"drives"`
		Interfaces []iface    `json:"network-interfaces,omitempty"`
		Machine    machine    `json:"machine-config"`
	}

	doc := document{
		BootSource: bootSource{KernelImagePath: cfg.Kernel, BootArgs: cfg.Cmdline},
		Drives: []drive{{
			DriveID:      "rootfs",
			PathOnHost:   cfg.Rootfs,
			IsRootDevice: true,
		}},
		Machine: machine{VCPUCount: cfg.VCPU, MemSizeMiB: cfg.MemoryMiB},
	}
	if cfg.Tap != "" {
		doc.Interfaces = []iface{{IfaceID: "eth0", HostDevName: cfg.Tap, GuestMAC: cfg.MAC}}
	}

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode firecracker config: %w", err)
	}
	if err := os.WriteFile(cfg.ConfigFile, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("write firecracker config: %w", err)
	}

	return []string{
		"--api-sock", cfg.APISocket,
		"--config-file", cfg.ConfigFile,
	}, nil
}
