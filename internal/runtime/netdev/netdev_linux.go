//go:build linux

package netdev

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	hostPrefix   = "msv-"
	peerPrefix   = "msvp-"
	tapPrefix    = "mst-"
	bridgePrefix = "msbr-"
	nftTable     = "marstack"
	nftNAT       = "marstack_nat"
	nftGuard     = "marstack_fw"
	guestIface   = "eth0"
	maxIfName    = 15
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
	if strings.Contains(existing, "ip daddr != "+cidr) {
		return nil
	}

	return run("nft", "add", "rule", "inet", nftTable, "postrouting",
		"ip", "saddr", cidr, "ip", "daddr", "!=", cidr, "oifname", "!=", bridge, "masquerade")
}

func (Datapath) ApplyRoutes(_ context.Context, routes []workload.Route) error {
	for _, route := range routes {
		if route.Slice == "" || route.Via == "" {
			continue
		}
		if err := run("ip", "route", "replace", route.Slice, "via", route.Via); err != nil {
			return err
		}
	}
	return nil
}

func (Datapath) ApplyForwards(_ context.Context, forwards []workload.Publish) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(renderForwards(forwards))

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply published ports: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func renderForwards(forwards []workload.Publish) string {
	var ruleset strings.Builder

	ruleset.WriteString("table ip " + nftNAT + " { }\n")
	ruleset.WriteString("delete table ip " + nftNAT + "\n")
	ruleset.WriteString("table ip " + nftNAT + " {\n")
	ruleset.WriteString("  chain prerouting {\n")
	ruleset.WriteString("    type nat hook prerouting priority dstnat; policy accept;\n")
	for _, publish := range forwards {
		if publish.Address == "" {
			continue
		}
		ruleset.WriteString(fmt.Sprintf("    %s dport %d dnat to %s:%d\n",
			publish.Protocol, publish.NodePort, publish.Address, publish.TargetPort))
	}
	ruleset.WriteString("  }\n}\n")

	return ruleset.String()
}

func (Datapath) ApplyGuards(_ context.Context, guards []workload.Guard) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(renderGuards(guards))

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply firewall rules: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func renderGuards(guards []workload.Guard) string {
	var ruleset strings.Builder

	ruleset.WriteString("table inet " + nftGuard + " { }\n")
	ruleset.WriteString("delete table inet " + nftGuard + "\n")
	ruleset.WriteString("table inet " + nftGuard + " {\n")

	for _, hook := range []string{"forward", "output"} {
		ruleset.WriteString("  chain " + hook + " {\n")
		ruleset.WriteString("    type filter hook " + hook + " priority filter; policy accept;\n")
		ruleset.WriteString("    ct state established,related accept\n")
		for _, guard := range guards {
			if guard.IP == "" {
				continue
			}
			for _, rule := range guard.Rules {
				ruleset.WriteString("    ip daddr " + guard.IP + " " + guardMatch(rule) + " accept\n")
			}
			ruleset.WriteString("    ip daddr " + guard.IP + " drop\n")
		}
		ruleset.WriteString("  }\n")
	}
	ruleset.WriteString("}\n")

	ruleset.WriteString("table bridge " + nftGuard + " { }\n")
	ruleset.WriteString("delete table bridge " + nftGuard + "\n")
	ruleset.WriteString("table bridge " + nftGuard + " {\n")
	ruleset.WriteString("  chain prerouting {\n")
	ruleset.WriteString("    type filter hook prerouting priority -290; policy accept;\n")
	for _, guard := range guards {
		if guard.IP == "" {
			continue
		}
		for _, rule := range guard.Rules {
			ruleset.WriteString("    ip daddr " + guard.IP + " " + guardMatch(rule) + " accept\n")
		}
		ruleset.WriteString("    ip daddr " + guard.IP +
			" tcp flags & (syn | ack) == syn drop\n")
		ruleset.WriteString("    ip daddr " + guard.IP + " icmp type echo-request drop\n")
	}
	ruleset.WriteString("  }\n}\n")

	return ruleset.String()
}

func guardMatch(rule workload.GuardRule) string {
	source := ""
	if rule.Source != "" && rule.Source != "0.0.0.0/0" {
		source = "ip saddr " + rule.Source + " "
	}

	switch rule.Protocol {
	case "any":
		return strings.TrimSpace(source)
	case "icmp":
		return source + "ip protocol icmp"
	default:
		if rule.FromPort == rule.ToPort {
			return fmt.Sprintf("%s%s dport %d", source, rule.Protocol, rule.FromPort)
		}
		return fmt.Sprintf("%s%s dport %d-%d", source, rule.Protocol, rule.FromPort, rule.ToPort)
	}
}

func (Datapath) ApplyFilters(_ context.Context, filters []workload.Filter) error {
	ruleset := renderFilters(filters)

	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply anti-spoof rules: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func renderFilters(filters []workload.Filter) string {
	var ruleset strings.Builder

	ruleset.WriteString("table bridge " + nftTable + " { }\n")
	ruleset.WriteString("delete table bridge " + nftTable + "\n")
	ruleset.WriteString("table bridge " + nftTable + " {\n")
	ruleset.WriteString("  chain prerouting {\n")
	ruleset.WriteString("    type filter hook prerouting priority -300; policy accept;\n")

	for _, filter := range filters {
		if filter.IP == "" || filter.MAC == "" {
			continue
		}
		port := hostName(filter.InstanceID)
		if filter.Isolation != "" && filter.Isolation != "container" {
			port = prefixed(tapPrefix, filter.InstanceID)
		}
		ruleset.WriteString(fmt.Sprintf("    iifname \"%s\" ether saddr != %s drop\n", port, filter.MAC))
		ruleset.WriteString(fmt.Sprintf("    iifname \"%s\" ip saddr != %s drop\n", port, filter.IP))
		ruleset.WriteString(fmt.Sprintf("    iifname \"%s\" arp saddr ip != %s drop\n", port, filter.IP))
	}

	ruleset.WriteString("  }\n}\n")
	return ruleset.String()
}

func (Datapath) Prune(_ context.Context, keep workload.Keep) error {
	present, err := managedLinks()
	if err != nil {
		return err
	}

	wanted := map[string]bool{}
	for _, bridge := range keep.Bridges {
		wanted[bridge] = true
	}
	for _, instanceID := range keep.Instances {
		wanted[hostName(instanceID)] = true
		wanted[prefixed(tapPrefix, instanceID)] = true
	}

	for _, link := range present {
		if wanted[link] {
			continue
		}
		if err := run("ip", "link", "del", link); err != nil {
			return err
		}
	}
	return nil
}

func managedLinks() ([]string, error) {
	out, err := output("ip", "-brief", "link", "show")
	if err != nil {
		return nil, err
	}

	links := make([]string, 0)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := strings.SplitN(fields[0], "@", 2)[0]
		for _, prefix := range []string{hostPrefix, tapPrefix, bridgePrefix} {
			if strings.HasPrefix(name, prefix) {
				links = append(links, name)
				break
			}
		}
	}
	return links, nil
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
		{"ip", "route", "add", cfg.Gateway, "dev", guestIface, "scope", "link"},
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

func TapName(instanceID string) string {
	return prefixed(tapPrefix, instanceID)
}

func EnsureTap(name, bridge string) error {
	if !linkExists(name) {
		if err := run("ip", "tuntap", "add", "dev", name, "mode", "tap"); err != nil {
			return err
		}
	}
	if err := run("ip", "link", "set", name, "master", bridge); err != nil {
		return err
	}
	return run("ip", "link", "set", name, "up")
}

func DeleteLink(name string) error {
	if !linkExists(name) {
		return nil
	}
	return run("ip", "link", "del", name)
}

func Detach(instanceID string) error {
	host := hostName(instanceID)
	if !linkExists(host) {
		return nil
	}
	return run("ip", "link", "del", host)
}
