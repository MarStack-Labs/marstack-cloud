//go:build linux

package microvm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func dualNICs() []nic {
	return []nic{
		{Tap: "mst-one", MAC: "02:00:00:00:00:01"},
		{Tap: "mst-one.1", MAC: "02:00:00:00:00:02"},
	}
}

func TestCloudHypervisorGetsOneNetFlagPerInterface(t *testing.T) {
	args, err := CloudHypervisor().Arguments(bootConfig{
		Kernel: "k", Rootfs: "r", VCPU: 1, MemoryMiB: 512,
		SerialSock: "s", APISocket: "a", NICs: dualNICs(),
	})
	if err != nil {
		t.Fatalf("arguments: %v", err)
	}

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--net tap=mst-one,mac=02:00:00:00:00:01") {
		t.Fatalf("the first interface is missing:\n%s", joined)
	}
	if !strings.Contains(joined, "--net tap=mst-one.1,mac=02:00:00:00:00:02") {
		t.Fatalf("the second interface is missing:\n%s", joined)
	}
	if strings.Count(joined, "--net ") != 2 {
		t.Fatalf("got %d --net flags, want one per interface:\n%s",
			strings.Count(joined, "--net "), joined)
	}
}

func TestFirecrackerGetsOneInterfaceEntryPerNIC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if _, err := Firecracker().Arguments(bootConfig{
		Kernel: "k", Rootfs: "r", VCPU: 1, MemoryMiB: 512,
		APISocket: "a", ConfigFile: path, NICs: dualNICs(),
	}); err != nil {
		t.Fatalf("arguments: %v", err)
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var doc struct {
		Interfaces []struct {
			IfaceID     string `json:"iface_id"`
			HostDevName string `json:"host_dev_name"`
			GuestMAC    string `json:"guest_mac"`
		} `json:"network-interfaces"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(doc.Interfaces) != 2 {
		t.Fatalf("interfaces = %d, want one per nic", len(doc.Interfaces))
	}
	if doc.Interfaces[0].IfaceID != "eth0" || doc.Interfaces[1].IfaceID != "eth1" {
		t.Fatalf("ids = %s, %s", doc.Interfaces[0].IfaceID, doc.Interfaces[1].IfaceID)
	}
	if doc.Interfaces[0].GuestMAC == doc.Interfaces[1].GuestMAC {
		t.Fatal("both interfaces share a mac, which is a bridge loop")
	}
	if doc.Interfaces[1].HostDevName != "mst-one.1" {
		t.Fatalf("the second interface points at %s", doc.Interfaces[1].HostDevName)
	}
}

func TestNoNICsMeansNoInterfaces(t *testing.T) {
	args, err := CloudHypervisor().Arguments(bootConfig{
		Kernel: "k", Rootfs: "r", VCPU: 1, MemoryMiB: 512, SerialSock: "s", APISocket: "a",
	})
	if err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if strings.Contains(strings.Join(args, " "), "--net") {
		t.Fatal("a --net flag appeared with no interfaces")
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := Firecracker().Arguments(bootConfig{
		Kernel: "k", Rootfs: "r", VCPU: 1, MemoryMiB: 512,
		APISocket: "a", ConfigFile: path,
	}); err != nil {
		t.Fatalf("arguments: %v", err)
	}

	blob, _ := os.ReadFile(path)
	if strings.Contains(string(blob), "network-interfaces") {
		t.Fatalf("an empty interface list was written:\n%s", blob)
	}
}

func TestTheGuestInitConfiguresEveryInterfaceAndOneDefaultRoute(t *testing.T) {
	for _, want := range []string{
		`while [ "$device" -lt "${MS_NICS:-0}" ]`,
		`eval "address=\$MS_IP_$device"`,
		`ip route add "$gateway" dev "$link"`,
		`[ "$device" = 0 ] && ip route add default via "$gateway"`,
	} {
		if !strings.Contains(guestInit, want) {
			t.Fatalf("the guest init is missing %q:\n%s", want, guestInit)
		}
	}
}

func TestSettingsCarryEveryInterfaceAndOneResolver(t *testing.T) {
	spec := workload.Spec{
		Name: "dual",
		Network: &workload.NetworkConfig{
			IP: "10.20.0.5", Prefix: 26, Gateway: "10.20.0.1",
			Nameserver: "10.20.0.1", SearchDomain: "default.internal",
		},
		Extra: []workload.NetworkConfig{{
			IP: "10.90.0.5", Prefix: 26, Gateway: "10.90.0.1",
			Nameserver: "10.90.0.1", SearchDomain: "backnet.internal",
		}},
	}

	nics := interfaces(spec)
	if len(nics) != 2 {
		t.Fatalf("interfaces = %d", len(nics))
	}
	if nics[1].Gateway != "10.90.0.1" {
		t.Fatalf("the extra interface lost its gateway: %+v", nics[1])
	}
}

func TestTheGuestInitConfiguresTheSecondFamilyToo(t *testing.T) {
	for _, want := range []string{
		`eval "address6=\$MS_IP6_$device"`,
		`ip -6 addr add "$address6" dev "$link"`,
		`ip -6 route add "$gateway6" dev "$link"`,
		`[ "$device" = 0 ] && ip -6 route add default via "$gateway6"`,
	} {
		if !strings.Contains(guestInit, want) {
			t.Fatalf("the guest init is missing %q:\n%s", want, guestInit)
		}
	}
}
