//go:build linux

package qemu

import (
	"strings"
	"testing"
)

func TestAGivenDeviceIsPassedToTheGuest(t *testing.T) {
	r := &Runtime{}

	spec := dualSpec()
	spec.DeviceAddress = "0000:81:00.0"

	joined := strings.Join(
		r.arguments(spec, "fw", "vars", "seed", "", []string{"mstap-one", "mstap-one.1"}, nil),
		" ")

	if !strings.Contains(joined, "vfio-pci,host=0000:81:00.0") {
		t.Fatalf("no vfio device in:\n%s", joined)
	}
}

func TestAGuestWithNoDeviceGetsNoVfio(t *testing.T) {
	r := &Runtime{}

	joined := strings.Join(
		r.arguments(dualSpec(), "fw", "vars", "seed", "",
			[]string{"mstap-one", "mstap-one.1"}, nil),
		" ")

	if strings.Contains(joined, "vfio-pci") {
		t.Fatalf("a guest that asked for nothing was given a device:\n%s", joined)
	}
}
