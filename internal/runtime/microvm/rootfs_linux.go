//go:build linux

package microvm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	initPath     = "/sbin/marstack-init"
	envPath      = "etc/marstack/env"
	commandPath  = "etc/marstack/command"
	rootfsSlackK = 64 * 1024
)

const guestInit = `#!/bin/sh
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sys /sys 2>/dev/null
mount -t devtmpfs dev /dev 2>/dev/null

[ -f /etc/marstack/env ] && . /etc/marstack/env

ip link set lo up 2>/dev/null

if [ -n "$MS_IP" ]; then
	ip addr add "$MS_IP" dev eth0
	ip link set eth0 up
	[ -n "$MS_GW" ] && ip route add "$MS_GW" dev eth0
	[ -n "$MS_GW" ] && ip route add default via "$MS_GW"
fi

if [ -n "$MS_DNS" ]; then
	printf 'nameserver %s\n' "$MS_DNS" > /etc/resolv.conf
	[ -n "$MS_SEARCH" ] && printf 'search %s\noptions ndots:1\n' "$MS_SEARCH" >> /etc/resolv.conf
fi

exec /bin/sh /etc/marstack/command
`

func (r *Runtime) prepareRootfs(ctx context.Context, spec workload.Spec) error {
	image := r.rootfsFile(spec.InstanceID)
	if _, err := os.Stat(image); err == nil {
		return nil
	}

	staging := filepath.Join(r.instanceDir(spec.InstanceID), "rootfs")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}

	config, err := r.images.Pull(ctx, spec.Image, staging)
	if err != nil {
		return err
	}

	command := spec.Command
	if len(command) == 0 {
		command = config.Command()
	}
	if len(command) == 0 {
		return fmt.Errorf("the image declares no command and none was given")
	}

	if err := writeGuestFiles(staging, spec, command, config.Env); err != nil {
		return err
	}

	if err := buildExt4(staging, image); err != nil {
		return err
	}

	return os.RemoveAll(staging)
}

func writeGuestFiles(staging string, spec workload.Spec, command, env []string) error {
	if err := os.MkdirAll(filepath.Join(staging, "etc", "marstack"), 0o755); err != nil {
		return fmt.Errorf("create guest config directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(staging, "sbin"), 0o755); err != nil {
		return fmt.Errorf("create guest sbin: %w", err)
	}

	if err := os.WriteFile(filepath.Join(staging, strings.TrimPrefix(initPath, "/")),
		[]byte(guestInit), 0o755); err != nil {
		return fmt.Errorf("write guest init: %w", err)
	}

	script := "#!/bin/sh\n"
	for _, variable := range env {
		name, value, found := strings.Cut(variable, "=")
		if !found {
			continue
		}
		script += "export " + name + "=" + shellQuote(value) + "\n"
	}
	script += "exec"
	for _, arg := range command {
		script += " " + shellQuote(arg)
	}
	script += "\n"

	if err := os.WriteFile(filepath.Join(staging, commandPath), []byte(script), 0o755); err != nil {
		return fmt.Errorf("write guest command: %w", err)
	}

	settings := ""
	if spec.Network != nil {
		settings += "MS_IP=" + shellQuote(spec.Network.IP+"/"+strconv.Itoa(spec.Network.Prefix)) + "\n"
		settings += "MS_GW=" + shellQuote(spec.Network.Gateway) + "\n"
		settings += "MS_DNS=" + shellQuote(spec.Network.Nameserver) + "\n"
		settings += "MS_SEARCH=" + shellQuote(spec.Network.SearchDomain) + "\n"
	}
	settings += "MS_NAME=" + shellQuote(spec.Name) + "\n"

	if err := os.WriteFile(filepath.Join(staging, envPath), []byte(settings), 0o644); err != nil {
		return fmt.Errorf("write guest environment: %w", err)
	}
	return nil
}

func buildExt4(staging, image string) error {
	size, err := directorySizeKB(staging)
	if err != nil {
		return err
	}

	build := exec.Command("mkfs.ext4",
		"-q", "-F",
		"-d", staging,
		"-b", "4096",
		image,
		strconv.Itoa(size+rootfsSlackK)+"k",
	)
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build root filesystem image: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func directorySizeKB(dir string) (int, error) {
	out, err := exec.Command("du", "-sk", dir).Output()
	if err != nil {
		return 0, fmt.Errorf("measure root filesystem: %w", err)
	}

	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("could not measure root filesystem")
	}

	size, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("could not read root filesystem size: %w", err)
	}
	return size, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
