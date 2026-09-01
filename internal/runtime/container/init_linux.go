//go:build linux

package container

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const gateFD = 3

func init() {
	if os.Getenv(initEnvConfig) == "" {
		return
	}
	if err := RunInit(); err != nil {
		fmt.Fprintln(os.Stderr, "container init:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

type initConfig struct {
	Hostname   string   `json:"hostname"`
	Rootfs     string   `json:"rootfs"`
	Command    []string `json:"command"`
	Env        []string `json:"env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

func RunInit() error {
	raw := os.Getenv(initEnvConfig)
	if raw == "" {
		return errors.New("container init was started without a configuration")
	}

	var cfg initConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("decode init config: %w", err)
	}
	if len(cfg.Command) == 0 {
		return errors.New("container init was given no command")
	}

	if err := syscall.Sethostname([]byte(cfg.Hostname)); err != nil {
		return fmt.Errorf("set hostname: %w", err)
	}
	if err := pivotInto(cfg.Rootfs); err != nil {
		return err
	}
	if err := mountProc(); err != nil {
		return err
	}
	if err := mountDev(); err != nil {
		return err
	}

	binary, err := resolveInRoot(cfg.Command[0])
	if err != nil {
		return err
	}

	if cfg.WorkingDir != "" {
		if err := syscall.Chdir(cfg.WorkingDir); err != nil {
			return fmt.Errorf("enter working directory %s: %w", cfg.WorkingDir, err)
		}
	}

	if err := waitForGate(); err != nil {
		return err
	}

	return syscall.Exec(binary, cfg.Command, environment(cfg.Env))
}

func environment(fromImage []string) []string {
	if len(fromImage) > 0 {
		return fromImage
	}
	return []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
}

func waitForGate() error {
	gate := os.NewFile(gateFD, "start-gate")
	defer gate.Close()

	buf := make([]byte, 1)
	if _, err := io.ReadFull(gate, buf); err != nil {
		return fmt.Errorf("the parent closed the start gate before the container was ready: %w", err)
	}
	return nil
}

func pivotInto(rootfs string) error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := syscall.Mount(rootfs, rootfs, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind rootfs: %w", err)
	}

	old := filepath.Join(rootfs, ".old-root")
	if err := os.MkdirAll(old, 0o700); err != nil {
		return fmt.Errorf("create pivot target: %w", err)
	}
	if err := syscall.PivotRoot(rootfs, old); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir after pivot: %w", err)
	}
	if err := syscall.Unmount("/.old-root", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	if err := os.Remove("/.old-root"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pivot target: %w", err)
	}
	return nil
}

func mountProc() error {
	if err := os.MkdirAll("/proc", 0o555); err != nil {
		return fmt.Errorf("create /proc: %w", err)
	}
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	if err := syscall.Mount("proc", "/proc", "proc", flags, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	return nil
}

func resolveInRoot(command string) (string, error) {
	if filepath.IsAbs(command) {
		if _, err := os.Stat(command); err != nil {
			return "", fmt.Errorf("command %s is not present in the image", command)
		}
		return command, nil
	}

	for _, dir := range []string{"/bin", "/usr/bin", "/sbin", "/usr/sbin"} {
		candidate := filepath.Join(dir, command)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("command %s was not found in the image", command)
}
