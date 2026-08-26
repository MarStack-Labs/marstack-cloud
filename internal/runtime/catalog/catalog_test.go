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
