package guest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func TestAFileLandsInsideTheRootfsWithItsMode(t *testing.T) {
	rootfs := t.TempDir()

	err := Drop(rootfs, []workload.FileDrop{
		{Path: "/etc/app/app.conf", Content: []byte("secret=1\n"), Mode: 0o640},
	})
	if err != nil {
		t.Fatalf("drop: %v", err)
	}

	written := filepath.Join(rootfs, "etc/app/app.conf")
	body, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != "secret=1\n" {
		t.Fatalf("content = %q", body)
	}

	info, err := os.Stat(written)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestASymlinkInTheImageCannotPullAFileOutOfTheRootfs(t *testing.T) {
	outside := t.TempDir()
	rootfs := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(rootfs, "etc")); err != nil {
		t.Fatalf("build the trap: %v", err)
	}

	err := Drop(rootfs, []workload.FileDrop{
		{Path: "/etc/passwd", Content: []byte("root::0:0::/:/bin/sh\n"), Mode: 0o644},
	})
	if err == nil {
		t.Fatal("writing through a symlink in the image was allowed, so an image can " +
			"choose where a config file lands on the host")
	}

	if _, err := os.Stat(filepath.Join(outside, "passwd")); err == nil {
		t.Fatal("the file landed outside the rootfs")
	}
}

func TestDroppingNothingDoesNothing(t *testing.T) {
	rootfs := t.TempDir()

	if err := Drop(rootfs, nil); err != nil {
		t.Fatalf("drop: %v", err)
	}

	entries, err := os.ReadDir(rootfs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the rootfs gained %d entries", len(entries))
	}
}
