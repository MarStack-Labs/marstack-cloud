package network

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
)

func parsePrefix(cidr string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse %q: %w", cidr, err)
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%q is not an IPv4 prefix", cidr)
	}
	return prefix.Masked(), nil
}

func gatewayOf(prefix netip.Prefix) netip.Addr {
	return prefix.Addr().Next()
}

func nextSlice(network netip.Prefix, taken map[string]bool) (netip.Prefix, error) {
	if network.Bits() >= SliceBits {
		return netip.Prefix{}, fmt.Errorf("network %s is too small to slice into /%d", network, SliceBits)
	}

	candidate := netip.PrefixFrom(network.Addr(), SliceBits).Masked()
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

	return netip.Prefix{}, fmt.Errorf("network %s has no free /%d slice left", network, SliceBits)
}

func advance(prefix netip.Prefix) (netip.Prefix, error) {
	step := uint32(1) << (32 - prefix.Bits())

	value := toUint32(prefix.Addr())
	if value+step < value {
		return netip.Prefix{}, fmt.Errorf("address space exhausted after %s", prefix)
	}

	return netip.PrefixFrom(toAddr(value+step), prefix.Bits()), nil
}

func toUint32(addr netip.Addr) uint32 {
	raw := addr.As4()
	return binary.BigEndian.Uint32(raw[:])
}

func toAddr(value uint32) netip.Addr {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], value)
	return netip.AddrFrom4(raw)
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
	host := uint32(1)<<(32-slice.Bits()) - 1
	return toUint32(addr) == toUint32(slice.Addr())+host
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
