package instance

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func movingInstance(t *testing.T) (*Module, string, string, string) {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()

	st, err := store.Open(ctx, filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard), nil)
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	m.UseDataDir(dir)

	made, err := m.Create(ctx, CreateParams{
		ProjectID: "prj-test",
		Name:      "db",
		Isolation: "vm",
		Image:     "ubuntu-24.04",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const nodeID = "n-1"
	if err := m.Assign(ctx, made.ID, nodeID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := m.BeginMigration(ctx, made.ID, nodeID); err != nil {
		t.Fatalf("begin migration: %v", err)
	}
	return m, made.ID, nodeID, dir
}

func parkedPath(dir, id string) string {
	return filepath.Join(dir, parkedDir, id+".qcow2")
}

func TestAParkedDiskIsWrittenWhereItCanBeFetched(t *testing.T) {
	m, id, nodeID, dir := movingInstance(t)
	ctx := context.Background()

	if err := m.svc.parkDisk(ctx, nodeID, id,
		strings.NewReader("the guest's disk")); err != nil {
		t.Fatalf("park: %v", err)
	}

	raw, err := os.ReadFile(parkedPath(dir, id))
	if err != nil {
		t.Fatalf("read the parked disk: %v", err)
	}
	if string(raw) != "the guest's disk" {
		t.Fatalf("parked = %q", raw)
	}
}

func TestALandedDiskIsNotLeftBehind(t *testing.T) {
	m, id, nodeID, dir := movingInstance(t)
	ctx := context.Background()

	if err := m.svc.parkDisk(ctx, nodeID, id,
		strings.NewReader("the guest's disk")); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := m.svc.finishMigration(ctx, nodeID, id); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if _, err := os.Stat(parkedPath(dir, id)); err == nil {
		t.Fatal("the copy on the control plane is still there after the disk landed. " +
			"Reading it is already refused by the flags, so nothing complains: it just " +
			"sits in the data directory, one whole vm disk per move, forever")
	}
}

func TestDeletingAnInstanceMidMoveTakesItsParkedDiskWithIt(t *testing.T) {
	m, id, nodeID, dir := movingInstance(t)
	ctx := context.Background()

	if err := m.svc.parkDisk(ctx, nodeID, id,
		strings.NewReader("the guest's disk")); err != nil {
		t.Fatalf("park: %v", err)
	}
	if err := m.Delete(ctx, id, "prj-test"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := os.Stat(parkedPath(dir, id)); err == nil {
		t.Fatal("the disk of a deleted instance is still parked on the control plane. " +
			"Nothing will ever come to fetch it and no state points at it, so it sits " +
			"in the data directory until somebody notices the disk filling up")
	}
}

func TestAHalfWrittenDiskIsNotLeftLookingComplete(t *testing.T) {
	m, id, nodeID, dir := movingInstance(t)
	ctx := context.Background()

	err := m.svc.parkDisk(ctx, nodeID, id, io.MultiReader(
		strings.NewReader("the start of a disk"),
		failingReader{},
	))
	if err == nil {
		t.Fatal("a transfer that broke half way through reported success")
	}

	if _, statErr := os.Stat(parkedPath(dir, id)); statErr == nil {
		t.Fatal("the half written file was left under the real name, so the next node to " +
			"look would take a truncated disk and boot it")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestADiskForAnInstanceWithAStrangeIdIsRefused(t *testing.T) {
	for _, id := range []string{"", "../escape", "a/b", "with.dot"} {
		if _, err := parkedName(id); err == nil {
			t.Errorf("%q was accepted as a file name, so a disk could be written outside "+
				"the directory meant for it", id)
		}
	}
}
