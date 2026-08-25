package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

func TestParseReference(t *testing.T) {
	cases := []struct {
		raw        string
		registry   string
		repository string
		tag        string
		digest     string
	}{
		{"alpine", defaultRegistry, "library/alpine", "latest", ""},
		{"alpine:3.20", defaultRegistry, "library/alpine", "3.20", ""},
		{"grafana/loki:3.0", defaultRegistry, "grafana/loki", "3.0", ""},
		{"ghcr.io/marstack-labs/mars:v1", "ghcr.io", "marstack-labs/mars", "v1", ""},
		{"localhost:5000/mine:dev", "localhost:5000", "mine", "dev", ""},
		{"registry.example.com:5000/team/app", "registry.example.com:5000", "team/app", "latest", ""},
		{
			"alpine@sha256:aaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccdddd",
			defaultRegistry, "library/alpine", "",
			"sha256:aaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccdddd",
		},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseReference(tc.raw)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			if got.Registry != tc.registry || got.Repository != tc.repository ||
				got.Tag != tc.tag || got.Digest != tc.digest {
				t.Fatalf("parsed %+v, want registry=%s repository=%s tag=%s digest=%s",
					got, tc.registry, tc.repository, tc.tag, tc.digest)
			}
		})
	}
}

func TestParseReferenceRejectsBadInput(t *testing.T) {
	for _, raw := range []string{"", "alpine@md5:abc"} {
		if _, err := ParseReference(raw); err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
	}
}

type fakeRegistry struct {
	t          *testing.T
	layers     [][]byte
	blobs      map[string][]byte
	realm      string
	config     any
	corrupt    bool
	tokenAsked bool
	served     map[string]int
}

func newFakeRegistry(t *testing.T, layers [][]byte) *fakeRegistry {
	return &fakeRegistry{t: t, layers: layers, blobs: map[string][]byte{}, served: map[string]int{}}
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (f *fakeRegistry) start() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenAsked = true
		if r.URL.Query().Get("scope") == "" {
			f.t.Error("token was requested without a scope")
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "let-me-in"})
	})

	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer let-me-in" {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="`+f.realm+`",service="fake",scope="repository:library/demo:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		f.served[r.URL.Path]++

		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			descriptors := make([]descriptor, 0, len(f.layers))
			for _, layer := range f.layers {
				digest := digestOf(layer)
				f.blobs[digest] = layer
				descriptors = append(descriptors, descriptor{
					MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
					Digest:    digest,
					Size:      int64(len(layer)),
				})
			}

			config := descriptor{}
			if f.config != nil {
				raw, _ := json.Marshal(f.config)
				digest := digestOf(raw)
				f.blobs[digest] = raw
				config = descriptor{Digest: digest, Size: int64(len(raw))}
			}

			w.Header().Set("Content-Type", ociManifest)
			json.NewEncoder(w).Encode(manifest{
				MediaType: ociManifest,
				Config:    config,
				Layers:    descriptors,
			})

		case strings.Contains(r.URL.Path, "/blobs/"):
			digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			blob, known := f.blobs[digest]
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if f.corrupt {
				blob = []byte("this is not the layer")
			}
			w.Write(blob)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	srv := httptest.NewServer(mux)
	f.realm = srv.URL + "/token"
	f.t.Cleanup(srv.Close)
	return srv
}

func layerWith(t *testing.T, entries map[string]string, gzipped bool) []byte {
	t.Helper()

	var raw bytes.Buffer
	archive := tar.NewWriter(&raw)

	for name, contents := range entries {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "/") {
			header = &tar.Header{Name: name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := archive.Write([]byte(contents)); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	if !gzipped {
		return raw.Bytes()
	}

	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	if _, err := writer.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	writer.Close()
	return zipped.Bytes()
}

func pullInto(t *testing.T, layers [][]byte) (string, *fakeRegistry) {
	t.Helper()

	fake := newFakeRegistry(t, layers)
	srv := fake.start()

	store := New(t.TempDir(), logging.New("error", io.Discard))
	store.registry = newRegistry(true)

	dest := t.TempDir()
	reference := strings.TrimPrefix(srv.URL, "http://") + "/library/demo:latest"

	if _, err := store.Pull(context.Background(), reference, dest); err != nil {
		t.Fatalf("pull: %v", err)
	}
	return dest, fake
}

func TestPullExtractsLayersInOrder(t *testing.T) {
	first := layerWith(t, map[string]string{"etc/config": "first", "bin/tool": "v1"}, true)
	second := layerWith(t, map[string]string{"bin/tool": "v2"}, true)

	dest, fake := pullInto(t, [][]byte{first, second})

	if !fake.tokenAsked {
		t.Error("the registry challenge was not answered with a token request")
	}

	config, err := os.ReadFile(filepath.Join(dest, "etc/config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(config) != "first" {
		t.Fatalf("config = %q, want it from the first layer", config)
	}

	tool, err := os.ReadFile(filepath.Join(dest, "bin/tool"))
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	if string(tool) != "v2" {
		t.Fatalf("tool = %q, want the later layer to win", tool)
	}
}

func TestPullAcceptsUncompressedLayers(t *testing.T) {
	dest, _ := pullInto(t, [][]byte{layerWith(t, map[string]string{"a": "plain"}, false)})

	if contents, err := os.ReadFile(filepath.Join(dest, "a")); err != nil || string(contents) != "plain" {
		t.Fatalf("contents = %q err = %v, want an uncompressed layer to be applied", contents, err)
	}
}

func TestWhiteoutDeletesAFileFromAnEarlierLayer(t *testing.T) {
	first := layerWith(t, map[string]string{"etc/keep": "yes", "etc/drop": "no"}, true)
	second := layerWith(t, map[string]string{"etc/.wh.drop": ""}, true)

	dest, _ := pullInto(t, [][]byte{first, second})

	if _, err := os.Stat(filepath.Join(dest, "etc/drop")); !os.IsNotExist(err) {
		t.Fatal("a whited out file survived")
	}
	if _, err := os.Stat(filepath.Join(dest, "etc/keep")); err != nil {
		t.Fatal("a whiteout removed the wrong file")
	}
}

func TestLayerCannotEscapeTheRootFilesystem(t *testing.T) {
	escape := layerWith(t, map[string]string{"../../etc/shadow": "pwned"}, true)

	fake := newFakeRegistry(t, [][]byte{escape})
	srv := fake.start()

	store := New(t.TempDir(), logging.New("error", io.Discard))
	store.registry = newRegistry(true)

	dest := t.TempDir()
	outside := filepath.Join(filepath.Dir(dest), "etc", "shadow")

	reference := strings.TrimPrefix(srv.URL, "http://") + "/library/demo:latest"
	_, _ = store.Pull(context.Background(), reference, dest)

	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a layer entry wrote outside the root filesystem")
	}
}

func TestBlobsAreCachedBetweenPulls(t *testing.T) {
	layer := layerWith(t, map[string]string{"a": "one"}, true)

	fake := newFakeRegistry(t, [][]byte{layer})
	srv := fake.start()

	store := New(t.TempDir(), logging.New("error", io.Discard))
	store.registry = newRegistry(true)

	reference := strings.TrimPrefix(srv.URL, "http://") + "/library/demo:latest"
	for range 2 {
		if _, err := store.Pull(context.Background(), reference, t.TempDir()); err != nil {
			t.Fatalf("pull: %v", err)
		}
	}

	blobPath := "/v2/library/demo/blobs/" + digestOf(layer)
	if served := fake.served[blobPath]; served != 1 {
		t.Fatalf("blob was fetched %d times, want 1: the cache did not hold", served)
	}
}

func TestCorruptBlobIsRejected(t *testing.T) {
	layer := layerWith(t, map[string]string{"a": "one"}, true)

	fake := newFakeRegistry(t, [][]byte{layer})
	fake.corrupt = true
	srv := fake.start()

	store := New(t.TempDir(), logging.New("error", io.Discard))
	store.registry = newRegistry(true)

	reference := strings.TrimPrefix(srv.URL, "http://") + "/library/demo:latest"
	_, err := store.Pull(context.Background(), reference, t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error = %v, want a digest mismatch", err)
	}
}

func TestSelectPlatformPrefersThisArchitecture(t *testing.T) {
	candidates := []descriptor{
		{Digest: "sha256:other"},
		{Digest: "sha256:mine"},
	}
	candidates[0].Platform.OS = "linux"
	candidates[0].Platform.Architecture = "riscv64"
	candidates[1].Platform.OS = "linux"
	candidates[1].Platform.Architecture = runtime.GOARCH

	chosen, err := selectPlatform(candidates)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if chosen.Digest != "sha256:mine" {
		t.Fatalf("chose %s, want the manifest for %s", chosen.Digest, runtime.GOARCH)
	}
}

func TestSelectPlatformFailsClearlyWhenAbsent(t *testing.T) {
	candidates := []descriptor{{Digest: "sha256:other"}}
	candidates[0].Platform.OS = "windows"
	candidates[0].Platform.Architecture = "amd64"

	if _, err := selectPlatform(candidates); err == nil {
		t.Fatal("expected an error naming the missing platform")
	} else if !strings.Contains(err.Error(), fmt.Sprintf("linux/%s", runtime.GOARCH)) {
		t.Fatalf("error = %v, want it to name the platform we need", err)
	}
}

func TestPullReturnsTheImageCommandAndEnvironment(t *testing.T) {
	fake := newFakeRegistry(t, [][]byte{layerWith(t, map[string]string{"bin/app": "x"}, true)})
	fake.config = map[string]any{
		"config": map[string]any{
			"Entrypoint": []string{"/bin/app"},
			"Cmd":        []string{"--serve"},
			"Env":        []string{"PATH=/bin", "APP_ENV=prod"},
			"WorkingDir": "/srv",
		},
	}
	srv := fake.start()

	store := New(t.TempDir(), logging.New("error", io.Discard))
	store.registry = newRegistry(true)

	reference := strings.TrimPrefix(srv.URL, "http://") + "/library/demo:latest"
	config, err := store.Pull(context.Background(), reference, t.TempDir())
	if err != nil {
		t.Fatalf("pull: %v", err)
	}

	command := config.Command()
	if len(command) != 2 || command[0] != "/bin/app" || command[1] != "--serve" {
		t.Fatalf("command = %v, want the entrypoint followed by the cmd", command)
	}
	if config.WorkingDir != "/srv" {
		t.Fatalf("working dir = %q, want /srv", config.WorkingDir)
	}
	if len(config.Env) != 2 {
		t.Fatalf("env = %v, want both variables", config.Env)
	}
}

func TestPullToleratesAnImageWithoutAConfig(t *testing.T) {
	dest, _ := pullInto(t, [][]byte{layerWith(t, map[string]string{"a": "one"}, true)})

	if _, err := os.Stat(filepath.Join(dest, "a")); err != nil {
		t.Fatalf("layer was not applied: %v", err)
	}
}
