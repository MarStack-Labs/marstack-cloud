//go:build linux

package container

import (
	"syscall"
	"testing"
)

func TestOnlyTheHarmlessDevicesAreThere(t *testing.T) {
	wanted := map[string]bool{
		"/dev/null": true, "/dev/zero": true, "/dev/full": true,
		"/dev/random": true, "/dev/urandom": true, "/dev/tty": true,
	}

	for _, dev := range devices {
		if !wanted[dev.path] {
			t.Errorf("%s is exposed to every container. A container gets the six devices "+
				"programs cannot work without and nothing else: this list is the boundary, "+
				"so anything like /dev/mem, /dev/kmsg or a loop device belongs nowhere "+
				"near it", dev.path)
		}
		delete(wanted, dev.path)
	}
	for path := range wanted {
		t.Errorf("%s is missing, and ordinary programs will fail without it", path)
	}
}

func TestEveryDeviceHasTheNumberLinuxExpects(t *testing.T) {
	known := map[string][2]uint32{
		"/dev/null": {1, 3}, "/dev/zero": {1, 5}, "/dev/full": {1, 7},
		"/dev/random": {1, 8}, "/dev/urandom": {1, 9}, "/dev/tty": {5, 0},
	}

	for _, dev := range devices {
		pair, ok := known[dev.path]
		if !ok {
			continue
		}
		if dev.major != pair[0] || dev.minor != pair[1] {
			t.Errorf("%s is %d:%d, want %d:%d. A wrong pair still creates a node, and "+
				"reading it gives whatever driver happens to answer",
				dev.path, dev.major, dev.minor, pair[0], pair[1])
		}
	}
}

func TestTheDeviceNumberIsEncodedTheWayMknodReads(t *testing.T) {
	for _, one := range []struct {
		major, minor, want uint32
	}{
		{1, 3, 0x103},
		{1, 9, 0x109},
		{5, 0, 0x500},
		{4, 260, 0x100404},
	} {
		if got := mkdev(one.major, one.minor); got != one.want {
			t.Errorf("mkdev(%d, %d) = %#x, want %#x", one.major, one.minor, got, one.want)
		}
	}
}

func TestDevItselfIsNotMountedNodev(t *testing.T) {
	if devFlags&syscall.MS_NODEV != 0 {
		t.Fatal("/dev is mounted nodev, which makes every node on it useless: they are " +
			"created and then nothing can be read from them")
	}
	if devFlags&syscall.MS_NOSUID == 0 {
		t.Error("/dev allows setuid, and nothing on it needs to")
	}
}

func TestSharedMemoryIsMountedTightly(t *testing.T) {
	for name, flag := range map[string]int{
		"nodev":  syscall.MS_NODEV,
		"noexec": syscall.MS_NOEXEC,
		"nosuid": syscall.MS_NOSUID,
	} {
		if shmFlags&flag == 0 {
			t.Errorf("/dev/shm is not %s, and it is a writable directory in every "+
				"container", name)
		}
	}
	if ptsFlags&syscall.MS_NOEXEC == 0 || ptsFlags&syscall.MS_NOSUID == 0 {
		t.Error("/dev/pts is mounted loosely")
	}
}

func TestTheStandardLinksArePresent(t *testing.T) {
	wanted := map[string]string{
		"/dev/fd":     "/proc/self/fd",
		"/dev/stdin":  "/proc/self/fd/0",
		"/dev/stdout": "/proc/self/fd/1",
		"/dev/stderr": "/proc/self/fd/2",
		"/dev/ptmx":   "pts/ptmx",
	}

	for _, link := range links {
		target, ok := wanted[link[1]]
		if !ok {
			t.Errorf("%s is linked and nothing asked for it", link[1])
			continue
		}
		if link[0] != target {
			t.Errorf("%s points at %s, want %s", link[1], link[0], target)
		}
		delete(wanted, link[1])
	}
	for path := range wanted {
		t.Errorf("%s is missing: a shell that redirects to it fails", path)
	}
}
