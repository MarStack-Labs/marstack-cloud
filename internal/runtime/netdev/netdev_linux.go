//go:build linux

package netdev

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	hostPrefix = "msv-"
	peerPrefix = "msvp-"
	nftTable   = "marstack"
	guestIface = "eth0"
	maxIfName  = 15
)

func hostName(instanceID string) string {
	return prefixed(hostPrefix, instanceID)
}

func peerName(instanceID string) string {
	return prefixed(peerPrefix, instanceID)
}

func prefixed(prefix, instanceID string) string {
	suffix := instanceID
	if idx := strings.IndexByte(suffix, '-'); idx >= 0 {
		suffix = suffix[idx+1:]
	}

	room := maxIfName - len(prefix)
	if len(suffix) > room {
		suffix = suffix[:room]
	}
	return prefix + suffix
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func linkExists(name string) bool {
	return exec.Command("ip", "link", "show", name).Run() == nil
}

func EnsureBridge(bridge, addr string) error {
	if !linkExists(bridge) {
		if err := run("ip", "link", "add", "name", bridge, "type", "bridge"); err != nil {
			return err
		}
	}

	if !hasAddress(bridge, addr) {
		if err := run("ip", "addr", "add", addr, "dev", bridge); err != nil {
			return err
		}
	}

	if err := run("ip", "link", "set", bridge, "up"); err != nil {
		return err
	}
	return enableForwarding()
}

func hasAddress(link, addr string) bool {
	out, err := output("ip", "-o", "addr", "show", "dev", link)
	if err != nil {
		return false
	}
	return strings.Contains(out, addr)
}

func enableForwarding() error {
	const path = "/proc/sys/net/ipv4/ip_forward"

	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(current)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
		return fmt.Errorf("enable ip forwarding: %w", err)
	}
	return nil
}

func EnsureEgress(bridge, cidr string) error {
	if err := run("nft", "add", "table", "inet", nftTable); err != nil {
		return err
	}
	if err := run("nft", "add", "chain", "inet", nftTable, "postrouting",
		"{ type nat hook postrouting priority srcnat; policy accept; }"); err != nil {
		return err
	}

	existing, err := output("nft", "list", "chain", "inet", nftTable, "postrouting")
	if err != nil {
		return err
	}
	if strings.Contains(existing, cidr) {
		return nil
	}

	return run("nft", "add", "rule", "inet", nftTable, "postrouting",
		"ip", "saddr", cidr, "oifname", "!=", bridge, "masquerade")
}

func Attach(pid int, cfg Interface) error {
	host := hostName(cfg.InstanceID)
	peer := peerName(cfg.InstanceID)

	if linkExists(host) {
		if err := run("ip", "link", "del", host); err != nil {
			return err
		}
	}

	if err := run("ip", "link", "add", host, "type", "veth", "peer", "name", peer); err != nil {
		return err
	}

	target := strconv.Itoa(pid)
	if err := run("ip", "link", "set", peer, "netns", target); err != nil {
		_ = run("ip", "link", "del", host)
		return err
	}

	if err := run("ip", "link", "set", host, "master", cfg.Bridge); err != nil {
		return err
	}
	if err := run("ip", "link", "set", host, "up"); err != nil {
		return err
	}

	address := cfg.IP + "/" + strconv.Itoa(cfg.Prefix)
	steps := [][]string{
		{"ip", "link", "set", peer, "name", guestIface},
		{"ip", "link", "set", "dev", guestIface, "address", cfg.MAC},
		{"ip", "addr", "add", address, "dev", guestIface},
		{"ip", "link", "set", guestIface, "up"},
		{"ip", "link", "set", "lo", "up"},
		{"ip", "route", "add", "default", "via", cfg.Gateway},
	}
	for _, step := range steps {
		args := append([]string{"--target", target, "--net", "--"}, step...)
		if err := run("nsenter", args...); err != nil {
			return err
		}
	}

	return nil
}

func Detach(instanceID string) error {
	host := hostName(instanceID)
	if !linkExists(host) {
		return nil
	}
	return run("ip", "link", "del", host)
}
