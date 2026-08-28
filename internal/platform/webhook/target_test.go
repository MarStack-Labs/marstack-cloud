package webhook

import (
	"strings"
	"testing"
)

func TestAURLMustBeSomethingWeCanPostTo(t *testing.T) {
	refused := map[string]string{
		"a bare host":       "example.com/hook",
		"an unknown scheme": "ftp://example.com/hook",
		"a file url":        "file:///etc/passwd",
		"no host":           "http://",
		"credentials in it": "http://user:pass@example.com/hook",
		"not a url at all":  "://///",
		"an over-long url":  "http://example.com/" + strings.Repeat("x", MaxURLLength),
	}

	for what, raw := range refused {
		if _, err := checkURL(raw); err == nil {
			t.Errorf("%s (%q) was accepted", what, raw)
		}
	}

	for _, raw := range []string{
		"http://example.com/hook",
		"https://example.com:8443/hook?x=1",
	} {
		if _, err := checkURL(raw); err != nil {
			t.Errorf("%q was refused: %v", raw, err)
		}
	}
}

func TestTheDialRefusesAddressesThatWouldTurnUsIntoAProxy(t *testing.T) {
	refused := map[string]string{
		"ipv4 loopback":     "127.0.0.1:7443",
		"ipv6 loopback":     "[::1]:7443",
		"cloud metadata":    "169.254.169.254:80",
		"link local ipv6":   "[fe80::1]:80",
		"the unspecified":   "0.0.0.0:80",
		"a multicast group": "224.0.0.1:80",
	}

	for what, address := range refused {
		if err := reachable(address); err == nil {
			t.Errorf("%s (%s) would have been dialled", what, address)
		}
	}
}

func TestTheDialAllowsThePrivateRangesTheFleetLivesIn(t *testing.T) {
	for _, address := range []string{
		"10.20.0.65:8080",
		"192.168.107.3:9000",
		"172.16.4.4:80",
		"[2001:db8::1]:443",
	} {
		if err := reachable(address); err != nil {
			t.Errorf("%s was refused, but that is where a private fleet lives: %v",
				address, err)
		}
	}
}

func TestASignatureIsStableAndKeyed(t *testing.T) {
	body := []byte(`{"kind":"instance.failed"}`)

	first := sign("whsec_one", body)
	if first != sign("whsec_one", body) {
		t.Fatal("the same secret and body signed differently twice")
	}
	if first == sign("whsec_two", body) {
		t.Fatal("two different secrets produced the same signature")
	}
	if !strings.HasPrefix(first, "sha256=") {
		t.Fatalf("signature = %q, want it to name its algorithm", first)
	}
}

func TestKindMatching(t *testing.T) {
	cases := []struct {
		kinds []string
		kind  string
		want  bool
	}{
		{nil, "instance.failed", true},
		{[]string{}, "anything", true},
		{[]string{"instance.failed"}, "instance.failed", true},
		{[]string{"instance.failed"}, "instance.running", false},
		{[]string{"instance.*"}, "instance.running", true},
		{[]string{"instance.*"}, "backend.down", false},
		{[]string{"backend.down", "node.*"}, "node.drained", true},
		{[]string{"instance.*"}, "instance", false},
	}

	for _, c := range cases {
		if got := matches(c.kinds, c.kind); got != c.want {
			t.Errorf("matches(%v, %q) = %v, want %v", c.kinds, c.kind, got, c.want)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	first := backoffFor(1)
	if first != FirstBackoff {
		t.Fatalf("first backoff = %v, want %v", first, FirstBackoff)
	}
	if backoffFor(2) <= first {
		t.Fatal("the second attempt does not wait longer than the first")
	}
	if capped := backoffFor(30); capped != MaxBackoff {
		t.Fatalf("backoff = %v, want it capped at %v", capped, MaxBackoff)
	}
}
