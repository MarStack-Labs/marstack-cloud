package pcidev

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	Root = "/sys/bus/pci/devices"

	Passthrough = "vfio-pci"

	ClassDisplay     = 0x03
	ClassProcessing  = 0x12
	ClassAccelerator = 0x1200
)

type Device struct {
	Address string
	Vendor  string
	Product string
	Class   string
	Kind    string
	Driver  string
	Ready   bool
}

func Scan(root string) ([]Device, error) {
	if root == "" {
		root = Root
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	held := make([]Device, 0, 4)
	for _, entry := range entries {
		one, wanted := read(filepath.Join(root, entry.Name()), entry.Name())
		if wanted {
			held = append(held, one)
		}
	}

	sort.Slice(held, func(i, j int) bool { return held[i].Address < held[j].Address })
	return held, nil
}

func read(dir, address string) (Device, bool) {
	class := value(dir, "class")
	kind, wanted := kindOf(class)
	if !wanted {
		return Device{}, false
	}

	driver := ""
	if link, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
		driver = filepath.Base(link)
	}

	return Device{
		Address: address,
		Vendor:  strings.TrimPrefix(value(dir, "vendor"), "0x"),
		Product: strings.TrimPrefix(value(dir, "device"), "0x"),
		Class:   strings.TrimPrefix(class, "0x"),
		Kind:    kind,
		Driver:  driver,
		Ready:   driver == Passthrough,
	}, true
}

func kindOf(class string) (string, bool) {
	raw, err := strconv.ParseUint(strings.TrimPrefix(class, "0x"), 16, 32)
	if err != nil {
		return "", false
	}

	switch raw >> 16 {
	case ClassDisplay:
		return "gpu", true
	case ClassProcessing:
		return "accelerator", true
	}
	return "", false
}

func value(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
