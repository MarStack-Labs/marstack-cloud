//go:build linux

package container

import (
	"fmt"
	"os"
	"syscall"
)

type device struct {
	path  string
	mode  uint32
	major uint32
	minor uint32
}

var devices = []device{
	{"/dev/null", 0o666, 1, 3},
	{"/dev/zero", 0o666, 1, 5},
	{"/dev/full", 0o666, 1, 7},
	{"/dev/random", 0o666, 1, 8},
	{"/dev/urandom", 0o666, 1, 9},
	{"/dev/tty", 0o666, 5, 0},
}

const (
	devFlags = syscall.MS_NOSUID | syscall.MS_STRICTATIME
	ptsFlags = syscall.MS_NOSUID | syscall.MS_NOEXEC
	shmFlags = syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC
)

var links = [][2]string{
	{"/proc/self/fd", "/dev/fd"},
	{"/proc/self/fd/0", "/dev/stdin"},
	{"/proc/self/fd/1", "/dev/stdout"},
	{"/proc/self/fd/2", "/dev/stderr"},
	{"pts/ptmx", "/dev/ptmx"},
}

func mountDev() error {
	if err := os.MkdirAll("/dev", 0o755); err != nil {
		return fmt.Errorf("create /dev: %w", err)
	}

	if err := syscall.Mount("tmpfs", "/dev", "tmpfs", devFlags,
		"mode=755,size=1m"); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}

	for _, dev := range devices {
		mode := dev.mode | syscall.S_IFCHR
		node := int(mkdev(dev.major, dev.minor))
		if err := syscall.Mknod(dev.path, mode, node); err != nil {
			return fmt.Errorf("create %s: %w", dev.path, err)
		}
		if err := os.Chmod(dev.path, os.FileMode(dev.mode)); err != nil {
			return fmt.Errorf("set the mode of %s: %w", dev.path, err)
		}
	}

	if err := mountPts(); err != nil {
		return err
	}
	if err := mountShm(); err != nil {
		return err
	}

	for _, link := range links {
		if err := os.Symlink(link[0], link[1]); err != nil {
			return fmt.Errorf("link %s: %w", link[1], err)
		}
	}
	return nil
}

func mkdev(major, minor uint32) uint32 {
	return (major&0xfff)<<8 | minor&0xff | (minor&0xfff00)<<12
}

func mountPts() error {
	if err := os.MkdirAll("/dev/pts", 0o755); err != nil {
		return fmt.Errorf("create /dev/pts: %w", err)
	}

	if err := syscall.Mount("devpts", "/dev/pts", "devpts", ptsFlags,
		"newinstance,ptmxmode=0666,mode=0620,gid=5"); err != nil {
		return fmt.Errorf("mount /dev/pts: %w", err)
	}
	return nil
}

func mountShm() error {
	if err := os.MkdirAll("/dev/shm", 0o755); err != nil {
		return fmt.Errorf("create /dev/shm: %w", err)
	}

	if err := syscall.Mount("shm", "/dev/shm", "tmpfs", shmFlags,
		"mode=1777,size="+shmSize); err != nil {
		return fmt.Errorf("mount /dev/shm: %w", err)
	}
	return nil
}
