//go:build linux

package qemu

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/runtime/console"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	consoleUser = "ubuntu"

	EnvironmentFile = "/etc/marstack/environment"
)

func writeFile(path string, mode uint32, content []byte) string {
	return "  - path: " + yamlScalar(path) + "\n" +
		fmt.Sprintf("    permissions: '%04o'\n", mode) +
		"    owner: root:root\n" +
		"    encoding: b64\n" +
		"    content: " + base64.StdEncoding.EncodeToString(content) + "\n"
}

func environmentFile(env map[string]string) string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)

	var out strings.Builder
	for _, name := range names {
		out.WriteString(name)
		out.WriteString("='")
		out.WriteString(strings.ReplaceAll(env[name], "'", `'\''`))
		out.WriteString("'\n")
	}
	return out.String()
}

func (r *Runtime) writeSeed(spec workload.Spec) (string, error) {
	dir := filepath.Join(r.instanceDir(spec.InstanceID), "seed")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create seed directory: %w", err)
	}

	meta := "instance-id: " + spec.InstanceID + "\nlocal-hostname: " + spec.Name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "meta-data"), []byte(meta), 0o600); err != nil {
		return "", fmt.Errorf("write meta-data: %w", err)
	}

	password, err := r.consolePassword(spec.InstanceID)
	if err != nil {
		return "", err
	}

	user := "#cloud-config\nhostname: " + spec.Name + "\nssh_pwauth: false\n" +
		"chpasswd:\n  expire: false\n  users:\n" +
		"    - {name: " + consoleUser + ", password: " + password + ", type: text}\n"
	if len(spec.SSHKeys) > 0 {
		user += "ssh_authorized_keys:\n"
		for _, key := range spec.SSHKeys {
			user += "  - " + yamlScalar(key) + "\n"
		}
	}
	if len(spec.Env) > 0 || len(spec.Files) > 0 {
		user += "write_files:\n"
	}
	if len(spec.Env) > 0 {
		user += writeFile(EnvironmentFile, 0o600,
			[]byte(environmentFile(spec.Env)))
	}
	for _, drop := range spec.Files {
		user += writeFile(drop.Path, drop.Mode, drop.Content)
	}
	if len(spec.Command) > 0 {
		user += "runcmd:\n  - " + shellQuote(spec.Command) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "user-data"), []byte(user), 0o600); err != nil {
		return "", fmt.Errorf("write user-data: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "network-config"),
		[]byte(networkConfig(spec)), 0o600); err != nil {
		return "", fmt.Errorf("write network-config: %w", err)
	}

	iso := filepath.Join(r.instanceDir(spec.InstanceID), "seed.iso")
	build := exec.Command("xorrisofs",
		"-output", iso,
		"-volid", "cidata",
		"-joliet", "-rock",
		filepath.Join(dir, "meta-data"),
		filepath.Join(dir, "user-data"),
		filepath.Join(dir, "network-config"),
	)
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build cloud-init seed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Chmod(iso, 0o600); err != nil {
		return "", fmt.Errorf("restrict the cloud-init seed: %w", err)
	}
	return iso, nil
}

func (r *Runtime) consolePassword(instanceID string) (string, error) {
	path := filepath.Join(r.instanceDir(instanceID), console.LoginName)

	if raw, err := os.ReadFile(path); err == nil {
		_, password, found := strings.Cut(strings.TrimSpace(string(raw)), ":")
		if found && password != "" {
			return password, nil
		}
	}

	password, err := secret()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(consoleUser+":"+password+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write the console login: %w", err)
	}
	return password, nil
}

func secret() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a console password: %w", err)
	}

	out := make([]byte, len(raw))
	for index, value := range raw {
		out[index] = alphabet[int(value)%len(alphabet)]
	}
	return string(out), nil
}

func networkConfig(spec workload.Spec) string {
	if spec.Network == nil {
		return "version: 2\nethernets: {}\n"
	}

	lines := []string{
		"version: 2",
		"ethernets:",
		"  primary:",
		"    match:",
		"      macaddress: " + spec.Network.MAC,
		"    set-name: eth0",
		fmt.Sprintf("    addresses: [%s/%d]", spec.Network.IP, spec.Network.Prefix),
		"    routes:",
		"      - to: default",
		"        via: " + spec.Network.Gateway,
		"        on-link: true",
	}

	if spec.Network.Nameserver != "" {
		lines = append(lines,
			"    nameservers:",
			"      addresses: ["+spec.Network.Nameserver+"]",
		)
		if spec.Network.SearchDomain != "" {
			lines = append(lines, "      search: ["+spec.Network.SearchDomain+"]")
		}
	}

	return strings.Join(lines, "\n") + "\n"
}

func yamlScalar(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func shellQuote(command []string) string {
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
