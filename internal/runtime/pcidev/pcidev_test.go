package pcidev

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeDevice(t *testing.T, root, address, class, vendor, product, driver string) {
	t.Helper()

	dir := filepath.Join(root, address)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for name, body := range map[string]string{
		"class": class, "vendor": vendor, "device": product,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if driver == "" {
		return
	}
	target := filepath.Join(root, "drivers", driver)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir driver: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "driver")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

func TestOnlyDevicesWorthPassingThroughAreListed(t *testing.T) {
	root := t.TempDir()

	fakeDevice(t, root, "0000:00:00.0", "0x060000", "0x106b", "0x1a05", "")
	fakeDevice(t, root, "0000:01:00.0", "0x030000", "0x10de", "0x2204", "vfio-pci")
	fakeDevice(t, root, "0000:02:00.0", "0x120000", "0x1234", "0x5678", "")
	fakeDevice(t, root, "0000:03:00.0", "0x020000", "0x8086", "0x10d3", "e1000e")

	held, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(held) != 2 {
		t.Fatalf("devices = %+v, want the display and processing ones only. A bridge or a "+
			"network card is not something anybody asks to be given", held)
	}
	if held[0].Kind != "gpu" || held[0].Address != "0000:01:00.0" {
		t.Fatalf("first = %+v, want the display controller", held[0])
	}
	if held[1].Kind != "accelerator" {
		t.Fatalf("second = %+v, want the processing accelerator", held[1])
	}
}

func TestOnlyADeviceBoundToVfioIsReady(t *testing.T) {
	root := t.TempDir()

	fakeDevice(t, root, "0000:01:00.0", "0x030000", "0x10de", "0x2204", "nvidia")
	fakeDevice(t, root, "0000:02:00.0", "0x030000", "0x10de", "0x2204", "vfio-pci")

	held, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if held[0].Ready {
		t.Fatalf("%+v is ready, but a device the host driver still holds cannot be given to "+
			"a guest: qemu would fail to open it, after the workload was already placed",
			held[0])
	}
	if !held[1].Ready {
		t.Fatalf("%+v is not ready, though vfio-pci is exactly what makes it passable",
			held[1])
	}
	if held[0].Driver != "nvidia" {
		t.Fatalf("driver = %q, want the one holding it named so an operator knows what to "+
			"unbind", held[0].Driver)
	}
}

func TestAMissingSysfsIsNotAnError(t *testing.T) {
	held, err := Scan(filepath.Join(t.TempDir(), "nothing-here"))
	if err != nil {
		t.Fatalf("scan: %v, want silence: a platform without pci is not a broken node", err)
	}
	if len(held) != 0 {
		t.Fatalf("devices = %+v, want none", held)
	}
}
