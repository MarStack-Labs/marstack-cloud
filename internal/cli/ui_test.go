package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
	kind byte
	link string
}

func archiveOf(t *testing.T, entries []entry) []byte {
	t.Helper()

	var raw bytes.Buffer
	zipped := gzip.NewWriter(&raw)
	archive := tar.NewWriter(zipped)

	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{
			Name:     e.name,
			Typeflag: kind,
			Mode:     0o644,
			Size:     int64(len(e.body)),
			Linkname: e.link,
		}
		if kind == tar.TypeDir || kind == tar.TypeSymlink {
			header.Size = 0
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if header.Size > 0 {
			if _, err := archive.Write([]byte(e.body)); err != nil {
				t.Fatalf("write %s: %v", e.name, err)
			}
		}
	}

	if err := archive.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return raw.Bytes()
}

func TestAConsoleArchiveUnpacksWithoutItsVersionDirectory(t *testing.T) {
	dir := t.TempDir()

	raw := archiveOf(t, []entry{
		{name: "marstack_console_v0.2.0/", kind: tar.TypeDir},
		{name: "marstack_console_v0.2.0/index.html", body: "<title>console</title>"},
		{name: "marstack_console_v0.2.0/meridian.css", body: ":root{}"},
		{name: "marstack_console_v0.2.0/LICENSE", body: "Apache"},
	})

	written, err := unpackConsole(bytes.NewReader(raw), dir)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}

	for _, name := range []string{"index.html", "meridian.css", "LICENSE"} {
		if !written[name] {
			t.Fatalf("unpack did not report %s, got %v", name, written)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("stat %s: %v: the version directory must be stripped, or --ui-dir "+
				"would have to name a path that changes every release", name, err)
		}
	}
}

func TestAnArchiveCannotWriteOutsideTheDirectoryItIsGiven(t *testing.T) {
	for _, name := range []string{
		"marstack_console_v0.2.0/../../escaped.txt",
		"../escaped.txt",
		"/etc/escaped.txt",
		`marstack_console_v0.2.0\..\..\escaped.txt`,
	} {
		dir := t.TempDir()
		raw := archiveOf(t, []entry{{name: name, body: "not yours"}})

		_, err := unpackConsole(bytes.NewReader(raw), dir)
		escaped := filepath.Join(filepath.Dir(dir), "escaped.txt")
		if _, statErr := os.Stat(escaped); statErr == nil {
			t.Fatalf("%s was unpacked outside %s", name, dir)
		}
		if err == nil {
			if _, statErr := os.Stat(filepath.Join(dir, "escaped.txt")); statErr == nil {
				continue
			}
			t.Fatalf("%s was accepted silently", name)
		}
	}
}

func TestAConsoleArchiveHoldsNothingButFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()

	raw := archiveOf(t, []entry{
		{name: "marstack_console_v0.2.0/index.html", body: "<title>console</title>"},
		{name: "marstack_console_v0.2.0/passwd", kind: tar.TypeSymlink, link: "/etc/passwd"},
	})

	if _, err := unpackConsole(bytes.NewReader(raw), dir); err == nil {
		t.Fatal("a symlink was unpacked, so an archive can point the console at any file " +
			"the control plane can read")
	}
}

func TestTheChecksumHasToBeTheOneTheReleasePublished(t *testing.T) {
	archive := []byte("pretend this is a console")
	sum := sha256.Sum256(archive)
	name := consoleArchive("v0.2.0")

	sums := []byte(
		hex.EncodeToString(sum[:]) + "  " + name + "\n" +
			"0000000000000000000000000000000000000000000000000000000000000000  other.tar.gz\n")

	if err := checkSum(archive, sums, name); err != nil {
		t.Fatalf("a matching checksum was refused: %v", err)
	}

	if err := checkSum([]byte("tampered"), sums, name); err == nil {
		t.Fatal("an archive that does not match its checksum was accepted")
	}

	err := checkSum(archive, sums, "missing.tar.gz")
	if err == nil {
		t.Fatal("an archive nothing vouches for was accepted")
	}
	if !strings.Contains(err.Error(), "nothing to check it against") {
		t.Fatalf("an unlisted archive was refused as though it had been tampered with: %v: "+
			"a missing checksum and a wrong one send the operator to different places", err)
	}
}

func TestInstallingRefusesAVersionThatWasNeverReleased(t *testing.T) {
	cmd := newUICmd()
	cmd.SetArgs([]string{"install", "--dir", t.TempDir()})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("install ran against a development version, which has no published console")
	}
	if !strings.Contains(err.Error(), "--version") {
		t.Fatalf("the refusal does not say how to proceed: %v", err)
	}
}
