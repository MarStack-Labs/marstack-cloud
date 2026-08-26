package artifact

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFetcher(t *testing.T) (*Fetcher, string) {
	t.Helper()
	dir := t.TempDir()
	return New(dir, slog.New(slog.NewTextHandler(io.Discard, nil))), dir
}

func serve(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func digestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestFetchVerifiesTheChecksum(t *testing.T) {
	fetcher, dir := newFetcher(t)
	payload := []byte("a disk image, honestly")
	srv := serve(t, payload)

	path, err := fetcher.Fetch(context.Background(), "good.qcow2", srv.URL, digestOf(payload))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if path != filepath.Join(dir, "good.qcow2") {
		t.Fatalf("path = %q", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(payload) {
		t.Fatalf("content = %q, err = %v", raw, err)
	}
}

func TestFetchVerifiesASHA512Checksum(t *testing.T) {
	fetcher, _ := newFetcher(t)
	payload := []byte("alpine publishes sha512")
	srv := serve(t, payload)

	sum := sha512.Sum512(payload)
	digest := "sha512:" + hex.EncodeToString(sum[:])

	if _, err := fetcher.Fetch(context.Background(), "alpine.qcow2", srv.URL, digest); err != nil {
		t.Fatalf("fetch: %v, want sha512 accepted because that is what Alpine signs", err)
	}

	wrong := "sha512:" + strings.Repeat("0", 128)
	if _, err := fetcher.Fetch(context.Background(), "other.qcow2", srv.URL, wrong); err == nil {
		t.Fatal("a mismatched sha512 was accepted")
	}
}

func TestFetchRejectsAMismatchedChecksum(t *testing.T) {
	fetcher, dir := newFetcher(t)
	srv := serve(t, []byte("not what was promised"))

	_, err := fetcher.Fetch(context.Background(), "bad.qcow2", srv.URL, digestOf([]byte("promised")))
	if err == nil {
		t.Fatal("a mismatched digest was accepted")
	}

	for _, name := range []string{"bad.qcow2", "bad.qcow2.part"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
			t.Fatalf("%s survived a failed verification, so a later boot would use it", name)
		}
	}
}

func TestFetchSkipsWhatIsAlreadyStaged(t *testing.T) {
	fetcher, dir := newFetcher(t)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "there.qcow2"), []byte("staged"), 0o644); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	path, err := fetcher.Fetch(context.Background(), "there.qcow2", "http://127.0.0.1:1/nope", "")
	if err != nil {
		t.Fatalf("fetch: %v, want the staged file used without touching the network", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "staged" {
		t.Fatalf("content = %q, err = %v", raw, err)
	}
}

func TestFetchReportsAServerError(t *testing.T) {
	fetcher, _ := newFetcher(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetcher.Fetch(context.Background(), "missing.qcow2", srv.URL, "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want it to name the status", err)
	}
}

func TestFetchRefusesAPathOutsideTheDirectory(t *testing.T) {
	fetcher, _ := newFetcher(t)

	for _, name := range []string{"../escape.qcow2", "sub/dir.qcow2"} {
		if _, err := fetcher.Fetch(context.Background(), name, "http://127.0.0.1:1/x", ""); err == nil {
			t.Fatalf("%q was accepted as a file name", name)
		}
	}
}
