//go:build linux

package qemu

import (
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func dualSpec() workload.Spec {
	return workload.Spec{
		InstanceID: "i-one",
		Name:       "dual",
		Network: &workload.NetworkConfig{
			Bridge: "msbr-a", BridgeAddr: "10.20.0.1/16",
			IP: "10.20.0.5", Prefix: 26, Gateway: "10.20.0.1",
			MAC: "02:00:00:00:00:01", Nameserver: "10.20.0.1", SearchDomain: "default.internal",
		},
		Extra: []workload.NetworkConfig{{
			Bridge: "msbr-b", BridgeAddr: "10.90.0.1/16",
			IP: "10.90.0.5", Prefix: 26, Gateway: "10.90.0.1",
			MAC: "02:00:00:00:00:02", Nameserver: "10.90.0.1", SearchDomain: "backnet.internal",
		}},
	}
}

func TestEachInterfaceGetsItsOwnNetdevAndMAC(t *testing.T) {
	r := &Runtime{}
	args := r.arguments(dualSpec(), "fw", "vars", "seed", "",
		[]string{"mstap-one", "mstap-one.1"}, nil)

	joined := strings.Join(args, " ")

	for _, want := range []string{
		"tap,id=net0,ifname=mstap-one,script=no,downscript=no",
		"virtio-net-pci,netdev=net0,mac=02:00:00:00:00:01,romfile=",
		"tap,id=net1,ifname=mstap-one.1,script=no,downscript=no",
		"virtio-net-pci,netdev=net1,mac=02:00:00:00:00:02,romfile=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestTheMACsAreNotShared(t *testing.T) {
	r := &Runtime{}
	args := r.arguments(dualSpec(), "fw", "vars", "seed", "",
		[]string{"mstap-one", "mstap-one.1"}, nil)

	joined := strings.Join(args, " ")
	if strings.Count(joined, "mac=02:00:00:00:00:01") != 1 {
		t.Fatalf("the first mac is used more than once, which is a bridge loop:\n%s", joined)
	}
}

func TestOnlyTheFirstInterfaceCarriesTheDefaultRoute(t *testing.T) {
	config := networkConfig(dualSpec())

	if strings.Count(config, "to: default") != 1 {
		t.Fatalf("network-config has %d default routes, want exactly one:\n%s",
			strings.Count(config, "to: default"), config)
	}
	if !strings.Contains(config, "  eth0:") || !strings.Contains(config, "  eth1:") {
		t.Fatalf("both interfaces should be configured:\n%s", config)
	}
	if !strings.Contains(config, "10.20.0.5/26") || !strings.Contains(config, "10.90.0.5/26") {
		t.Fatalf("both addresses should be set:\n%s", config)
	}
}

func TestOnlyTheFirstInterfaceCarriesTheResolver(t *testing.T) {
	config := networkConfig(dualSpec())

	if strings.Count(config, "nameservers:") != 1 {
		t.Fatalf("two resolvers configured, and which one answers is up to the guest:\n%s",
			config)
	}
	if strings.Contains(config, "backnet.internal") {
		t.Fatalf("the extra interface set a search domain:\n%s", config)
	}
}

func TestOneInterfaceRendersExactlyWhatItUsedTo(t *testing.T) {
	spec := dualSpec()
	spec.Extra = nil

	config := networkConfig(spec)
	for _, want := range []string{
		"  eth0:", "macaddress: 02:00:00:00:00:01", "set-name: eth0",
		"addresses: [10.20.0.5/26]", "to: default", "via: 10.20.0.1",
		"addresses: [10.20.0.1]", "search: [default.internal]",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("missing %q in:\n%s", want, config)
		}
	}
	if strings.Contains(config, "eth1") {
		t.Fatalf("a second interface appeared with none asked for:\n%s", config)
	}
}

func TestNoNetworkStillRendersAnEmptyEthernets(t *testing.T) {
	config := networkConfig(workload.Spec{InstanceID: "i-one"})

	if !strings.Contains(config, "ethernets: {}") {
		t.Fatalf("config = %q", config)
	}
}

func TestAnExtraInterfaceCanReachItsGateway(t *testing.T) {
	config := networkConfig(dualSpec())
	eth1 := config[strings.Index(config, "  eth1:"):]

	if !strings.Contains(eth1, "to: 10.90.0.1/32") || !strings.Contains(eth1, "scope: link") {
		t.Fatalf("eth1 has no link route to its gateway:\n%s\nthe gateway sits outside the "+
			"slice the guest is given, so without one the guest answers arp and nothing else",
			eth1)
	}
	if strings.Contains(eth1, "to: 10.90.0.5/26") {
		t.Fatalf("eth1 routes to its own address with host bits set, which netplan refuses, "+
			"taking the whole file down and leaving eth0 unconfigured too:\n%s", eth1)
	}
	if !strings.Contains(eth1, "addresses: [10.90.0.5/26]") {
		t.Fatalf("eth1 lost its address:\n%s", eth1)
	}
}
