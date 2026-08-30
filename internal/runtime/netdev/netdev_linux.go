//go:build linux

package netdev

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	hostPrefix   = "msv-"
	peerPrefix   = "msvp-"
	tapPrefix    = "mst-"
	bridgePrefix = "msbr-"
	nftTable     = "marstack"
	nftNAT       = "marstack_nat"
	nftNAT6      = "marstack_nat6"
	nftGuard     = "marstack_fw"
	guestIface   = "eth0"
	maxIfName    = 15
	balancedMark = "0x1"
)

func hostName(instanceID string, device int) string {
	return withDevice(prefixed(hostPrefix, instanceID), device)
}

func peerName(instanceID string, device int) string {
	return withDevice(prefixed(peerPrefix, instanceID), device)
}

func withDevice(name string, device int) string {
	if device == 0 {
		return name
	}

	suffix := "." + strconv.Itoa(device)
	if len(name)+len(suffix) > maxIfName {
		name = name[:maxIfName-len(suffix)]
	}
	return name + suffix
}

func guestName(device int) string {
	return "eth" + strconv.Itoa(device)
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

var egressMu sync.Mutex

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
	for _, path := range []string{
		"/proc/sys/net/ipv4/ip_forward",
		"/proc/sys/net/ipv6/conf/all/forwarding",
	} {
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if strings.TrimSpace(string(current)) == "1" {
			continue
		}
		if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
			return fmt.Errorf("enable forwarding at %s: %w", path, err)
		}
	}
	return nil
}

func family(address string) string {
	if strings.Contains(address, ":") {
		return "ip6"
	}
	return "ip"
}

func EnsureEgress(bridge, cidr string) error {
	egressMu.Lock()
	defer egressMu.Unlock()

	if err := run("nft", "add", "table", "inet", nftTable); err != nil {
		return err
	}
	if err := run("nft", "add", "chain", "inet", nftTable, "postrouting",
		"{ type nat hook postrouting priority srcnat; policy accept; }"); err != nil {
		return err
	}

	listed, err := output("nft", "list", "chain", "inet", nftTable, "postrouting")
	if err != nil {
		return err
	}

	present := masqueradeRules(listed)
	unique := deduped(append(present, egressRule(bridge, cidr)))

	if len(unique) == len(present) {
		return nil
	}
	return rewritePostrouting(unique)
}

func egressRule(bridge, cidr string) string {
	network := masked(cidr)
	proto := family(network)

	return fmt.Sprintf("%s saddr %s %s daddr != %s oifname != \"%s\" masquerade",
		proto, network, proto, network, bridge)
}

func masked(cidr string) string {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return cidr
	}
	return prefix.Masked().String()
}

func deduped(rules []string) []string {
	seen := make(map[string]bool, len(rules))
	unique := make([]string, 0, len(rules))

	for _, rule := range rules {
		if seen[rule] {
			continue
		}
		seen[rule] = true
		unique = append(unique, rule)
	}
	return unique
}

func masqueradeRules(listed string) []string {
	rules := make([]string, 0, 4)
	for _, line := range strings.Split(listed, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "masquerade") && strings.Contains(line, "saddr") {
			rules = append(rules, line)
		}
	}
	return rules
}

func rewritePostrouting(rules []string) error {
	var ruleset strings.Builder
	ruleset.WriteString("flush chain inet " + nftTable + " postrouting\n")
	ruleset.WriteString("table inet " + nftTable + " {\n")
	ruleset.WriteString("  chain postrouting {\n")
	for _, rule := range rules {
		ruleset.WriteString("    " + rule + "\n")
	}
	ruleset.WriteString("  }\n}\n")

	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset.String())

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rewrite the egress rules: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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

	four, six := splitByFamily(forwards)
	ruleset.WriteString(natTable("ip", nftNAT, four))
	ruleset.WriteString(natTable("ip6", nftNAT6, six))

	return ruleset.String()
}

func splitByFamily(forwards []workload.Publish) (four, six []workload.Publish) {
	for _, publish := range forwards {
		address := publish.Address
		if len(publish.Targets) > 0 {
			address = publish.Targets[0]
		}
		if family(address) == "ip6" {
			six = append(six, publish)
			continue
		}
		four = append(four, publish)
	}
	return four, six
}

func natTable(proto, name string, forwards []workload.Publish) string {
	var ruleset strings.Builder

	ruleset.WriteString("table " + proto + " " + name + " { }\n")
	ruleset.WriteString("delete table " + proto + " " + name + "\n")
	ruleset.WriteString("table " + proto + " " + name + " {\n")
	ruleset.WriteString("  chain prerouting {\n")
	ruleset.WriteString("    type nat hook prerouting priority dstnat; policy accept;\n")
	for _, publish := range forwards {
		rule := forwardRule(publish)
		if rule == "" {
			continue
		}
		ruleset.WriteString("    " + rule + "\n")
	}
	ruleset.WriteString("  }\n")
	ruleset.WriteString("  chain postrouting {\n")
	ruleset.WriteString("    type nat hook postrouting priority srcnat; policy accept;\n")
	ruleset.WriteString("    ct mark " + balancedMark + " masquerade\n")
	ruleset.WriteString("  }\n}\n")

	return ruleset.String()
}

func forwardRule(publish workload.Publish) string {
	if len(publish.Targets) == 0 {
		if publish.Address == "" {
			return ""
		}
		return fmt.Sprintf("%s dport %d dnat to %s:%d",
			publish.Protocol, publish.NodePort,
			bracketed(publish.Address), publish.TargetPort)
	}

	proto := family(publish.Targets[0])
	entries := make([]string, 0, len(publish.Targets))
	for i, address := range publish.Targets {
		if family(address) != proto {
			return ""
		}
		entries = append(entries, fmt.Sprintf("%d : %s . %d", i, address, publish.TargetPort))
	}

	return fmt.Sprintf("%s dport %d ct mark set %s dnat to %s mod %d map { %s }",
		publish.Protocol, publish.NodePort, balancedMark,
		selector(publish.Algorithm, proto),
		len(publish.Targets), strings.Join(entries, ", "))
}

func bracketed(address string) string {
	if family(address) == "ip6" {
		return "[" + address + "]"
	}
	return address
}

func selector(algorithm, proto string) string {
	if algorithm == "source_hash" {
		return "jhash " + proto + " saddr"
	}
	return "numgen inc"
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
			proto := family(guard.IP)
			for _, rule := range guard.Rules {
				match, usable := guardMatch(proto, rule)
				if !usable {
					continue
				}
				ruleset.WriteString("    " + proto + " daddr " + guard.IP + " " +
					match + " accept\n")
			}
			ruleset.WriteString("    " + proto + " daddr " + guard.IP + " drop\n")
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
		proto := family(guard.IP)
		for _, rule := range guard.Rules {
			match, usable := guardMatch(proto, rule)
			if !usable {
				continue
			}
			ruleset.WriteString("    " + proto + " daddr " + guard.IP + " " +
				match + " accept\n")
		}
		ruleset.WriteString("    " + proto + " daddr " + guard.IP +
			" tcp flags & (syn | ack) == syn drop\n")
		ruleset.WriteString("    " + proto + " daddr " + guard.IP + " " +
			echoRequest(proto) + " drop\n")
	}
	ruleset.WriteString("  }\n}\n")

	return ruleset.String()
}

func echoRequest(proto string) string {
	if proto == "ip6" {
		return "icmpv6 type echo-request"
	}
	return "icmp type echo-request"
}

func guardMatch(proto string, rule workload.GuardRule) (string, bool) {
	source := ""
	if rule.Source != "" && rule.Source != "0.0.0.0/0" && rule.Source != "::/0" {
		if family(rule.Source) != proto {
			return "", false
		}
		source = proto + " saddr " + rule.Source + " "
	}

	switch rule.Protocol {
	case "any":
		return strings.TrimSpace(source), true
	case "icmp":
		if proto == "ip6" {
			return source + "meta l4proto ipv6-icmp", true
		}
		return source + "ip protocol icmp", true
	default:
		if rule.FromPort == rule.ToPort {
			return fmt.Sprintf("%s%s dport %d", source, rule.Protocol, rule.FromPort), true
		}
		return fmt.Sprintf("%s%s dport %d-%d",
			source, rule.Protocol, rule.FromPort, rule.ToPort), true
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
		port := hostName(filter.InstanceID, filter.Device)
		if filter.Isolation != "" && filter.Isolation != "container" {
			port = TapName(filter.InstanceID, filter.Device)
		}
		ruleset.WriteString(fmt.Sprintf(
			"    iifname \"%s\" ether saddr != %s drop\n", port, filter.MAC))
		ruleset.WriteString(fmt.Sprintf("    iifname \"%s\" %s saddr != %s drop\n",
			port, family(filter.IP), filter.IP))

		if filter.IP6 != "" {
			ruleset.WriteString(fmt.Sprintf("    iifname \"%s\" ip6 saddr != %s drop\n",
				port, filter.IP6))
		}

		if family(filter.IP) == "ip6" {
			continue
		}
		ruleset.WriteString(fmt.Sprintf(
			"    iifname \"%s\" arp saddr ip != %s drop\n", port, filter.IP))
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
		for device := range MaxDevices {
			wanted[hostName(instanceID, device)] = true
			wanted[TapName(instanceID, device)] = true
		}
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
	host := hostName(cfg.InstanceID, cfg.Device)
	peer := peerName(cfg.InstanceID, cfg.Device)
	guest := guestName(cfg.Device)

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
		{"ip", "link", "set", peer, "name", guest},
		{"ip", "link", "set", "dev", guest, "address", cfg.MAC},
		{"ip", "addr", "add", address, "dev", guest},
		{"ip", "link", "set", guest, "up"},
		{"ip", "link", "set", "lo", "up"},
		{"ip", "route", "add", cfg.Gateway, "dev", guest, "scope", "link"},
	}
	if cfg.Device == 0 {
		steps = append(steps, []string{"ip", "route", "add", "default", "via", cfg.Gateway})
	}

	if cfg.IP6 != "" {
		second := cfg.IP6 + "/" + strconv.Itoa(cfg.Prefix6)
		steps = append(steps,
			[]string{"ip", "-6", "addr", "add", second, "dev", guest},
			[]string{"ip", "-6", "route", "add", cfg.Gateway6, "dev", guest},
		)
		if cfg.Device == 0 {
			steps = append(steps,
				[]string{"ip", "-6", "route", "add", "default", "via", cfg.Gateway6})
		}
	}
	for _, step := range steps {
		args := append([]string{"--target", target, "--net", "--"}, step...)
		if err := run("nsenter", args...); err != nil {
			return err
		}
	}

	return nil
}

func TapName(instanceID string, device int) string {
	return withDevice(prefixed(tapPrefix, instanceID), device)
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
	for device := range MaxDevices {
		host := hostName(instanceID, device)
		if !linkExists(host) {
			continue
		}
		if err := run("ip", "link", "del", host); err != nil {
			return err
		}
	}
	return nil
}

func (Datapath) ApplyEgress(_ context.Context, networks []workload.Egress) error {
	for _, n := range networks {
		if n.Bridge == "" {
			continue
		}
		for _, address := range []string{n.Gateway, n.Gateway6} {
			if address == "" {
				continue
			}
			if err := EnsureBridge(n.Bridge, address); err != nil {
				return err
			}
			if err := EnsureEgress(n.Bridge, address); err != nil {
				return err
			}
		}
	}
	return nil
}
