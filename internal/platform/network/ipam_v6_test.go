package network

import (
	"net/netip"
	"testing"
)

func mustPrefix(t *testing.T, cidr string) netip.Prefix {
	t.Helper()

	prefix, err := parsePrefix(cidr)
	if err != nil {
		t.Fatalf("parse %s: %v", cidr, err)
	}
	return prefix
}

func TestSlicesWalkForwardInBothFamilies(t *testing.T) {
	cases := map[string][]string{
		"10.20.0.0/16": {"10.20.0.64/26", "10.20.0.128/26", "10.20.0.192/26"},
		"fd00:dead::/48": {
			"fd00:dead:0:1::/64",
			"fd00:dead:0:2::/64",
			"fd00:dead:0:3::/64",
		},
	}

	for cidr, want := range cases {
		network := mustPrefix(t, cidr)
		taken := map[string]bool{}

		for _, expected := range want {
			got, err := nextSlice(network, taken)
			if err != nil {
				t.Fatalf("%s: next slice: %v", cidr, err)
			}
			if got.String() != expected {
				t.Fatalf("%s: slice = %s, want %s", cidr, got, expected)
			}
			taken[got.String()] = true
		}
	}
}

func TestAddressesWalkForwardInBothFamilies(t *testing.T) {
	cases := map[string][]string{
		"10.20.0.64/26":      {"10.20.0.65", "10.20.0.66", "10.20.0.67"},
		"fd00:dead:0:1::/64": {"fd00:dead:0:1::1", "fd00:dead:0:1::2", "fd00:dead:0:1::3"},
	}

	for cidr, want := range cases {
		slice := mustPrefix(t, cidr)
		taken := map[string]bool{}

		for _, expected := range want {
			got, err := nextAddress(slice, taken)
			if err != nil {
				t.Fatalf("%s: next address: %v", cidr, err)
			}
			if got.String() != expected {
				t.Fatalf("%s: address = %s, want %s", cidr, got, expected)
			}
			taken[got.String()] = true
		}
	}
}

func TestTheV4BroadcastIsSkippedAndV6HasNone(t *testing.T) {
	slice := mustPrefix(t, "10.20.0.64/30")
	taken := map[string]bool{"10.20.0.65": true, "10.20.0.66": true}

	if _, err := nextAddress(slice, taken); err == nil {
		t.Fatal("10.20.0.67 was handed out, and that is the broadcast address")
	}

	six := mustPrefix(t, "fd00:dead:0:1::/126")
	taken = map[string]bool{"fd00:dead:0:1::1": true, "fd00:dead:0:1::2": true}

	got, err := nextAddress(six, taken)
	if err != nil {
		t.Fatalf("v6 refused its last address: %v", err)
	}
	if got.String() != "fd00:dead:0:1::3" {
		t.Fatalf("address = %s, want the last one: v6 has no broadcast to reserve", got)
	}
}

func TestCarryingAcrossAByteBoundary(t *testing.T) {
	network := mustPrefix(t, "10.20.0.0/16")

	taken := map[string]bool{}
	var last netip.Prefix
	for range 4 {
		got, err := nextSlice(network, taken)
		if err != nil {
			t.Fatalf("next slice: %v", err)
		}
		taken[got.String()] = true
		last = got
	}

	if last.String() != "10.20.1.0/26" {
		t.Fatalf("fourth slice = %s, want the carry into the third octet", last)
	}
}

func TestAnExhaustedSpaceIsAnErrorRatherThanAWrap(t *testing.T) {
	if _, carried := addPowerOfTwo(netip.MustParseAddr("255.255.255.255"), 0); !carried {
		t.Fatal("adding one to the last v4 address did not report a carry")
	}

	last := netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")
	if _, carried := addPowerOfTwo(last, 0); !carried {
		t.Fatal("adding one to the last v6 address did not report a carry")
	}
}

func TestAV4MappedPrefixIsRefused(t *testing.T) {
	if _, err := parsePrefix("::ffff:10.20.0.0/112"); err == nil {
		t.Fatal("a v4 address written as v6 was accepted, and it would allocate as v6")
	}
}
