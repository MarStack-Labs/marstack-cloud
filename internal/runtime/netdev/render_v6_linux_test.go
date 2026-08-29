//go:build linux

package netdev

import (
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func TestGuardsRenderInTheFamilyOfTheirAddress(t *testing.T) {
	out := renderGuards([]workload.Guard{
		{IP: "10.20.0.5", Rules: []workload.GuardRule{
			{Protocol: "tcp", FromPort: 80, ToPort: 80},
		}},
		{IP: "fd00:dead:0:1::5", Rules: []workload.GuardRule{
			{Protocol: "tcp", FromPort: 443, ToPort: 443},
		}},
	})

	if !strings.Contains(out, "ip daddr 10.20.0.5 tcp dport 80 accept") {
		t.Fatalf("the v4 guard is missing:\n%s", out)
	}
	if !strings.Contains(out, "ip6 daddr fd00:dead:0:1::5 tcp dport 443 accept") {
		t.Fatalf("the v6 guard is missing or in the wrong family:\n%s", out)
	}
	if strings.Contains(out, "ip daddr fd00") {
		t.Fatal("a v6 address was rendered with the ip family, which nft refuses to load, " +
			"taking every other instance's rules down with it")
	}
	if !strings.Contains(out, "icmpv6 type echo-request") {
		t.Fatalf("the v6 ping drop uses the v4 icmp keyword:\n%s", out)
	}
}

func TestASourceOfTheWrongFamilyIsDroppedRatherThanRendered(t *testing.T) {
	out := renderGuards([]workload.Guard{
		{IP: "fd00:dead:0:1::5", Rules: []workload.GuardRule{
			{Protocol: "tcp", FromPort: 443, ToPort: 443, Source: "10.0.0.0/8"},
			{Protocol: "tcp", FromPort: 8443, ToPort: 8443, Source: "fd00::/8"},
		}},
	})

	if strings.Contains(out, "10.0.0.0/8") {
		t.Fatal("a v4 source was rendered into a v6 rule, which does not load")
	}
	if !strings.Contains(out, "ip6 saddr fd00::/8 tcp dport 8443 accept") {
		t.Fatalf("the matching-family rule is missing:\n%s", out)
	}
}

func TestAntiSpoofUsesTheRightFamilyAndSkipsArpForV6(t *testing.T) {
	out := renderFilters([]workload.Filter{
		{InstanceID: "i-one", IP: "10.20.0.5", MAC: "02:00:00:00:00:01"},
		{InstanceID: "i-two", IP: "fd00:dead:0:1::5", MAC: "02:00:00:00:00:02", Device: 1},
	})

	if !strings.Contains(out, "ip saddr != 10.20.0.5 drop") {
		t.Fatalf("the v4 anti-spoof rule is missing:\n%s", out)
	}
	if !strings.Contains(out, "ip6 saddr != fd00:dead:0:1::5 drop") {
		t.Fatalf("the v6 anti-spoof rule is missing:\n%s", out)
	}
	if strings.Contains(out, "arp saddr ip != fd00") {
		t.Fatal("an arp rule was rendered for a v6 address, and IPv6 has no arp")
	}
}

func TestForwardRuleRendersV6WithBracketsAndFamily(t *testing.T) {
	single := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80, Address: "fd00:dead:0:1::5",
	})
	if single != "tcp dport 8080 dnat to [fd00:dead:0:1::5]:80" {
		t.Fatalf("rule = %q", single)
	}

	four := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80, Address: "10.20.0.5",
	})
	if four != "tcp dport 8080 dnat to 10.20.0.5:80" {
		t.Fatalf("rule = %q", four)
	}
}

func TestEachFamilyGetsItsOwnNatTable(t *testing.T) {
	out := renderForwards([]workload.Publish{
		{Protocol: "tcp", NodePort: 80, TargetPort: 80, Address: "10.20.0.5"},
		{Protocol: "tcp", NodePort: 443, TargetPort: 443, Address: "fd00:dead:0:1::5"},
	})

	if !strings.Contains(out, "table ip marstack_nat {") {
		t.Fatalf("the v4 table is missing:\n%s", out)
	}
	if !strings.Contains(out, "table ip6 marstack_nat6 {") {
		t.Fatalf("the v6 table is missing:\n%s", out)
	}

	four := out[strings.Index(out, "table ip marstack_nat {"):strings.Index(out, "table ip6")]
	if strings.Contains(four, "fd00") {
		t.Fatal("a v6 rule was put in the ip table, which nft refuses, taking every " +
			"published port down with it")
	}

	six := out[strings.Index(out, "table ip6 marstack_nat6 {"):]
	if strings.Contains(six, "10.20.0.5") {
		t.Fatal("a v4 rule was put in the ip6 table")
	}
}

func TestAnEmptyFamilyStillGetsItsTableSoOldRulesAreCleared(t *testing.T) {
	out := renderForwards([]workload.Publish{
		{Protocol: "tcp", NodePort: 80, TargetPort: 80, Address: "10.20.0.5"},
	})

	if !strings.Contains(out, "delete table ip6 marstack_nat6") {
		t.Fatalf("the v6 table is not reset, so a rule survives the balancer that made it:\n%s",
			out)
	}
}

func TestAMixedFamilyBackendSetIsRefusedRatherThanRendered(t *testing.T) {
	out := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80,
		Targets: []string{"10.20.0.5", "fd00:dead:0:1::5"},
	})

	if out != "" {
		t.Fatalf("rule = %q, want nothing: one nftables map holds one address type, and a "+
			"rule that does not load takes every published port with it", out)
	}
}

func TestSourceHashHashesTheRightFamily(t *testing.T) {
	out := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80, Algorithm: "source_hash",
		Targets: []string{"fd00:dead:0:1::5", "fd00:dead:0:1::6"},
	})

	if !strings.Contains(out, "jhash ip6 saddr") {
		t.Fatalf("rule = %q, want the hash taken over the v6 source", out)
	}
}
