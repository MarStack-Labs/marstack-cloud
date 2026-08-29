package network

import (
	"crypto/rand"
	"fmt"
	"net/netip"
	"strings"
)

func parsePrefix(cidr string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse %q: %w", cidr, err)
	}
	if prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf(
			"%q is an IPv4 address written as IPv6; write it as IPv4", cidr)
	}
	return prefix.Masked(), nil
}

func gatewayOf(prefix netip.Prefix) netip.Addr {
	return prefix.Addr().Next()
}

func sliceBitsFor(network netip.Prefix) int {
	if network.Addr().Is4() {
		return SliceBits
	}
	return SliceBitsV6
}

func nextSlice(network netip.Prefix, taken map[string]bool) (netip.Prefix, error) {
	bits := sliceBitsFor(network)
	if network.Bits() >= bits {
		return netip.Prefix{}, fmt.Errorf(
			"network %s is too small to slice into /%d", network, bits)
	}

	candidate := netip.PrefixFrom(network.Addr(), bits).Masked()
	for range reservedSlices {
		next, err := advance(candidate)
		if err != nil {
			return netip.Prefix{}, err
		}
		candidate = next
	}

	for network.Contains(candidate.Addr()) {
		if !taken[candidate.String()] {
			return candidate, nil
		}
		next, err := advance(candidate)
		if err != nil {
			return netip.Prefix{}, err
		}
		candidate = next
	}

	return netip.Prefix{}, fmt.Errorf("network %s has no free /%d slice left", network, bits)
}

func advance(prefix netip.Prefix) (netip.Prefix, error) {
	width := prefix.Addr().BitLen()

	next, carried := addPowerOfTwo(prefix.Addr(), width-prefix.Bits())
	if carried {
		return netip.Prefix{}, fmt.Errorf("address space exhausted after %s", prefix)
	}
	return netip.PrefixFrom(next, prefix.Bits()), nil
}

func addPowerOfTwo(addr netip.Addr, exponent int) (netip.Addr, bool) {
	raw := addr.AsSlice()
	byteIndex := len(raw) - 1 - exponent/8
	if byteIndex < 0 {
		return addr, true
	}

	carry := byte(1) << (exponent % 8)
	for i := byteIndex; i >= 0; i-- {
		sum := raw[i] + carry
		wrapped := sum < raw[i]
		raw[i] = sum

		if !wrapped {
			carry = 0
			break
		}
		carry = 1
	}
	if carry != 0 {
		return addr, true
	}

	out, _ := netip.AddrFromSlice(raw)
	return out, false
}

func nextAddress(slice netip.Prefix, taken map[string]bool) (netip.Addr, error) {
	candidate := slice.Addr().Next()

	for slice.Contains(candidate) {
		if isBroadcast(slice, candidate) {
			break
		}
		if !taken[candidate.String()] {
			return candidate, nil
		}
		candidate = candidate.Next()
	}

	return netip.Addr{}, fmt.Errorf("slice %s has no free address left", slice)
}

func isBroadcast(slice netip.Prefix, addr netip.Addr) bool {
	if !slice.Addr().Is4() {
		return false
	}

	last, carried := addPowerOfTwo(slice.Addr(), 32-slice.Bits())
	if carried {
		return false
	}
	return addr == last.Prev()
}

func randomMAC() (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate mac: %w", err)
	}

	parts := []string{"02"}
	for _, b := range buf {
		parts = append(parts, fmt.Sprintf("%02x", b))
	}
	return strings.Join(parts, ":"), nil
}

func bridgeName(networkID string) string {
	suffix := networkID
	if idx := strings.IndexByte(suffix, '-'); idx >= 0 {
		suffix = suffix[idx+1:]
	}
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return bridgePrefix + suffix
}

func overlapsAny(candidate netip.Prefix, taken []netip.Prefix) bool {
	for _, other := range taken {
		if other.Overlaps(candidate) {
			return true
		}
	}
	return false
}

func freePrefix(supernet netip.Prefix, bits int, taken []netip.Prefix) (netip.Prefix, error) {
	if supernet.Bits() > bits {
		return netip.Prefix{}, fmt.Errorf("supernet %s cannot hold a /%d", supernet, bits)
	}

	candidate := netip.PrefixFrom(supernet.Addr(), bits).Masked()
	for supernet.Contains(candidate.Addr()) {
		if !overlapsAny(candidate, taken) {
			return candidate, nil
		}
		next, err := advance(candidate)
		if err != nil {
			return netip.Prefix{}, err
		}
		candidate = next
	}

	return netip.Prefix{}, fmt.Errorf("supernet %s has no free /%d left", supernet, bits)
}
