package catalog

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newCatalog(t *testing.T) (*Catalog, string) {
	t.Helper()
	dir := t.TempDir()
	return New(dir, slog.New(slog.NewTextHandler(io.Discard, nil))), dir
}

func TestFileNamePerKind(t *testing.T) {
	cases := map[string]Image{
		"ubuntu-24.04.qcow2": {Name: "ubuntu-24.04", Kind: KindDisk},
		"alpine.iso":         {Name: "alpine", Kind: KindISO},
		"linux-6.8.Image":    {Name: "linux-6.8", Kind: KindKernel},
	}

	for want, in := range cases {
		if got := FileName(in); got != want {
			t.Fatalf("FileName(%+v) = %q, want %q", in, got, want)
		}
	}
}

func TestStageRejectsTheWrongKind(t *testing.T) {
	c, _ := newCatalog(t)
	c.Replace([]Image{{Name: "installer", Kind: KindISO, Source: "https://example.test/a.iso"}})

	_, err := c.Stage(context.Background(), "installer", KindDisk)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("err = %v, want it to say the kind is wrong", err)
	}
}

func TestStageExplainsAnUnregisteredImage(t *testing.T) {
	c, _ := newCatalog(t)

	_, err := c.Stage(context.Background(), "ubuntu-24.04", KindDisk)
	if err == nil {
		t.Fatal("an unregistered image was accepted")
	}
	if !strings.Contains(err.Error(), "marstack image create") {
		t.Fatalf("err = %v, want it to say how to fix it", err)
	}
}

func TestStageUsesAFileStagedByHand(t *testing.T) {
	c, dir := newCatalog(t)

	staged := filepath.Join(dir, "ubuntu-24.04.qcow2")
	if err := os.WriteFile(staged, []byte("disk"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	path, err := c.Stage(context.Background(), "ubuntu-24.04", KindDisk)
	if err != nil {
		t.Fatalf("stage: %v, want a file put there by an operator to keep working", err)
	}
	if path != staged {
		t.Fatalf("path = %q, want %q", path, staged)
	}
}

func TestStageFindsAnImageByID(t *testing.T) {
	c, dir := newCatalog(t)
	c.Replace([]Image{{ID: "img-1", Name: "ubuntu-24.04", Kind: KindDisk,
		Source: "http://127.0.0.1:1/x"}})

	if err := os.WriteFile(filepath.Join(dir, "ubuntu-24.04.qcow2"), []byte("disk"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if _, err := c.Stage(context.Background(), "img-1", KindDisk); err != nil {
		t.Fatalf("stage by id: %v", err)
	}
}

func TestPruneKeepsWhatAnOperatorStaged(t *testing.T) {
	c, dir := newCatalog(t)

	byHand := filepath.Join(dir, "ubuntu-24.04.qcow2")
	if err := os.WriteFile(byHand, []byte("disk"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if err := c.Prune(nil); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(byHand); err != nil {
		t.Fatal("a file staged by hand was deleted: it has no origin, so nothing proves we own it")
	}
}

func TestPruneRemovesWhatLeftTheCatalog(t *testing.T) {
	c, dir := newCatalog(t)

	staged := filepath.Join(dir, "gone.qcow2")
	if err := os.WriteFile(staged, []byte("disk"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(staged+".origin", []byte("img-gone\n"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	c.Replace([]Image{{ID: "img-other", Name: "other", Kind: KindDisk}})

	if err := c.Prune(nil); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(staged); err == nil {
		t.Fatal("an image the catalog no longer knows kept its disk space")
	}
	if _, err := os.Stat(staged + ".origin"); err == nil {
		t.Fatal("the origin marker outlived its image")
	}
}

func TestPruneKeepsAnImageStillInUse(t *testing.T) {
	c, dir := newCatalog(t)

	staged := filepath.Join(dir, "running.qcow2")
	if err := os.WriteFile(staged, []byte("disk"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(staged+".origin", []byte("img-running\n"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	c.Replace(nil)

	if err := c.Prune([]string{"running"}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatal("the backing file of a running vm was deleted, which breaks the guest")
	}
}

func TestStagedReportsWhatTheAgentDownloaded(t *testing.T) {
	c, dir := newCatalog(t)

	if err := os.WriteFile(filepath.Join(dir, "a.qcow2"), []byte("1234"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.qcow2.origin"), []byte("img-a"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.qcow2"), []byte("by hand"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	staged, err := c.Staged()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged = %+v, want only the file with an origin", staged)
	}
	if staged[0].Origin != "img-a" || staged[0].Bytes != 4 {
		t.Fatalf("staged = %+v", staged[0])
	}
}
