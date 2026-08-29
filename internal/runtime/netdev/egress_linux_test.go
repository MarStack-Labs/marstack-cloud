//go:build linux

package netdev

import (
	"strings"
	"testing"
)

func TestMasqueradeRulesAreLiftedOutOfAChainListing(t *testing.T) {
	listed := `table inet marstack {
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ct mark 0x00000001 masquerade
		ip saddr 10.20.0.0/16 ip daddr != 10.20.0.0/16 oifname != "msbr-a" masquerade
		ip6 saddr fd00::/48 ip6 daddr != fd00::/48 oifname != "msbr-b" masquerade
	}
}`

	got := masqueradeRules(listed)
	if len(got) != 2 {
		t.Fatalf("rules = %v, want the two with a saddr and not the ct mark one", got)
	}
	if !strings.HasPrefix(got[0], "ip saddr") || !strings.HasPrefix(got[1], "ip6 saddr") {
		t.Fatalf("rules = %v", got)
	}
}

func TestTheRuleTextMatchesWhatNftPrints(t *testing.T) {
	four := egressRule("msbr-w076ym7q", "10.20.0.0/16")
	want := `ip saddr 10.20.0.0/16 ip daddr != 10.20.0.0/16 oifname != "msbr-w076ym7q" masquerade`
	if four != want {
		t.Fatalf("rule  = %q\nwant  = %q\nif these differ the rule is added again every pass", four, want)
	}

	six := egressRule("msbr-9vp5hk9k", "fd00:dead:beef::/48")
	wantSix := `ip6 saddr fd00:dead:beef::/48 ip6 daddr != fd00:dead:beef::/48 ` +
		`oifname != "msbr-9vp5hk9k" masquerade`
	if six != wantSix {
		t.Fatalf("rule  = %q\nwant  = %q", six, wantSix)
	}
}

func TestDuplicatesAreFoundEvenWhenNotAdjacent(t *testing.T) {
	rules := []string{"a", "b", "a", "c"}

	got := deduped(rules)
	if len(got) != 3 {
		t.Fatalf("deduped = %v, want three: the two copies of a are not next to each other, "+
			"which is exactly the case a consecutive-only compaction misses", got)
	}
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("deduped = %v, want the first occurrence of each kept in order", got)
	}
}

func TestAChainThatIsAlreadyRightIsLeftAlone(t *testing.T) {
	present := []string{
		`ip saddr 10.20.0.0/16 ip daddr != 10.20.0.0/16 oifname != "msbr-a" masquerade`,
	}
	wanted := egressRule("msbr-a", "10.20.0.0/16")

	if len(deduped(append(present, wanted))) != len(present) {
		t.Fatal("a chain that already holds the rule would be rewritten every pass")
	}
}

func TestARealChainWithADuplicateIsDetected(t *testing.T) {
	listed := "table inet marstack {\n" +
		"\tchain postrouting {\n" +
		"\t\ttype nat hook postrouting priority srcnat; policy accept;\n" +
		"\t\tip saddr 10.20.0.0/16 ip daddr != 10.20.0.0/16 oifname != \"msbr-w076ym7q\" masquerade\n" +
		"\t\tip saddr 10.0.0.0/16 ip daddr != 10.0.0.0/16 oifname != \"msbr-b1vytybd\" masquerade\n" +
		"\t\tip saddr 10.90.0.0/16 ip daddr != 10.90.0.0/16 oifname != \"msbr-2kk51sk0\" masquerade\n" +
		"\t\tip6 saddr fd00:dead:beef::/48 ip6 daddr != fd00:dead:beef::/48 oifname != \"msbr-9vp5hk9k\" masquerade\n" +
		"\t\tip saddr 10.0.0.0/16 ip daddr != 10.0.0.0/16 oifname != \"msbr-b1vytybd\" masquerade\n" +
		"\t}\n}"

	present := masqueradeRules(listed)
	if len(present) != 5 {
		t.Fatalf("parsed %d rules from a chain holding 5", len(present))
	}

	unique := deduped(append(present, egressRule("msbr-9vp5hk9k", "fd00:dead:beef::/48")))
	if len(unique) != 4 {
		t.Fatalf("unique = %d, want 4", len(unique))
	}
	if len(unique) == len(present) {
		t.Fatal("the rewrite would be skipped, so the duplicate would live forever")
	}
}

func TestTheRuleIsRenderedFromTheNetworkNotTheGateway(t *testing.T) {
	fromGateway := egressRule("msbr-b1vytybd", "10.0.0.1/16")
	fromNetwork := egressRule("msbr-b1vytybd", "10.0.0.0/16")

	if fromGateway != fromNetwork {
		t.Fatalf("gateway form = %q\nnetwork form = %q\nnft stores the masked form, so a rule "+
			"rendered from the gateway never matches what is already there and is added again "+
			"on every single call", fromGateway, fromNetwork)
	}

	six := egressRule("msbr-9vp5hk9k", "fd00:dead:beef::1/48")
	if !strings.Contains(six, "fd00:dead:beef::/48") {
		t.Fatalf("rule = %q, want the masked v6 network", six)
	}
}
