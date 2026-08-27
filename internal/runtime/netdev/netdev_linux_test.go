//go:build linux

package netdev

import (
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func TestASingleTargetStaysAPlainRule(t *testing.T) {
	rule := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80, Address: "10.20.0.5",
	})

	want := "tcp dport 8080 dnat to 10.20.0.5:80"
	if rule != want {
		t.Fatalf("rule = %q, want %q", rule, want)
	}
}

func TestAPublishWithNoAddressRendersNothing(t *testing.T) {
	if rule := forwardRule(workload.Publish{Protocol: "tcp", NodePort: 8080}); rule != "" {
		t.Fatalf("rule = %q, want nothing to be written for an instance with no address", rule)
	}
}

func TestSeveralTargetsBecomeARoundRobinMap(t *testing.T) {
	rule := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80,
		Targets: []string{"10.20.0.5", "10.20.0.6", "10.20.0.7"},
	})

	want := "tcp dport 8080 ct mark set 0x1 dnat to numgen inc mod 3 map " +
		"{ 0 : 10.20.0.5 . 80, 1 : 10.20.0.6 . 80, 2 : 10.20.0.7 . 80 }"
	if rule != want {
		t.Fatalf("rule = %q, want %q", rule, want)
	}
}

func TestSourceHashPinsAClientToOneBackend(t *testing.T) {
	rule := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80, Algorithm: "source_hash",
		Targets: []string{"10.20.0.5", "10.20.0.6"},
	})

	if !strings.Contains(rule, "jhash ip saddr mod 2") {
		t.Fatalf("rule = %q, want it keyed on the source address", rule)
	}
}

func TestAnUnknownAlgorithmFallsBackToRoundRobin(t *testing.T) {
	rule := forwardRule(workload.Publish{
		Protocol: "tcp", NodePort: 8080, TargetPort: 80, Algorithm: "least_conn",
		Targets: []string{"10.20.0.5", "10.20.0.6"},
	})

	if !strings.Contains(rule, "numgen inc mod 2") {
		t.Fatalf("rule = %q, want round robin rather than an invalid ruleset", rule)
	}
}

func TestOneTargetStillUsesAMap(t *testing.T) {
	rule := forwardRule(workload.Publish{
		Protocol: "udp", NodePort: 5353, TargetPort: 53,
		Targets: []string{"10.20.0.5"},
	})

	want := "udp dport 5353 ct mark set 0x1 dnat to numgen inc mod 1 map { 0 : 10.20.0.5 . 53 }"
	if rule != want {
		t.Fatalf("rule = %q, want %q", rule, want)
	}
}

func TestTheRulesetCarriesEveryPublish(t *testing.T) {
	ruleset := renderForwards([]workload.Publish{
		{Protocol: "tcp", NodePort: 22, TargetPort: 22, Address: "10.20.0.5"},
		{Protocol: "tcp", NodePort: 80, TargetPort: 80,
			Targets: []string{"10.20.0.6", "10.20.0.7"}},
	})

	if !strings.Contains(ruleset, "dport 22 dnat to 10.20.0.5:22") {
		t.Fatalf("ruleset lost the single forward:\n%s", ruleset)
	}
	if !strings.Contains(ruleset, "dport 80 ct mark set 0x1 dnat to numgen inc mod 2") {
		t.Fatalf("ruleset lost the balanced forward:\n%s", ruleset)
	}
	if !strings.Contains(ruleset, "ct mark 0x1 masquerade") {
		t.Fatalf("ruleset has no masquerade, so a backend on another node would reply "+
			"straight to the client and the connection would never establish:\n%s", ruleset)
	}
	if strings.Contains(ruleset, "dport 22 ct mark set") {
		t.Fatalf("a single-target forward was marked, which would hide the client "+
			"address for no reason:\n%s", ruleset)
	}
	if !strings.Contains(ruleset, "delete table ip "+nftNAT) {
		t.Fatalf("ruleset does not replace the table, so old rules would survive:\n%s", ruleset)
	}
}
