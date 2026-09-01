package image

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

func pulledOnce(t *testing.T, root string) (*Store, *fakeRegistry, string) {
	t.Helper()

	layer := layerWith(t, map[string]string{"a": "one"}, true)
	fake := newFakeRegistry(t, [][]byte{layer})
	srv := fake.start()

	store := New(root, logging.New("error", io.Discard))
	store.registry = newRegistry(true)

	reference := strings.TrimPrefix(srv.URL, "http://") + "/library/demo:latest"
	if _, err := store.Pull(context.Background(), reference, t.TempDir()); err != nil {
		t.Fatalf("first pull: %v", err)
	}
	return store, fake, reference
}

func unreachable(store *Store) *brokenTransport {
	broken := &brokenTransport{}
	store.registry = newRegistry(true)
	store.registry.http.Transport = broken
	return broken
}

var errUnreachable = errors.New("the registry cannot be reached")

type brokenTransport struct {
	tried int
}

func (b *brokenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	b.tried++
	return nil, errUnreachable
}

func TestANodeStartsAnImageItAlreadyHoldsWhenTheRegistryIsGone(t *testing.T) {
	root := t.TempDir()
	store, _, reference := pulledOnce(t, root)

	unreachable(store)

	if _, err := store.Pull(context.Background(), reference, t.TempDir()); err != nil {
		t.Fatalf("second pull: %v: every layer was on disk and the node still could not "+
			"start a container it holds all the bytes for", err)
	}
}

func TestTheRegistryIsStillAskedFirst(t *testing.T) {
	root := t.TempDir()
	_, fake, _ := pulledOnce(t, root)

	store := New(root, logging.New("error", io.Discard))
	store.registry = newRegistry(true)

	reference := strings.TrimPrefix(fake.realm, "http://")
	reference = strings.TrimSuffix(reference, "/token") + "/library/demo:latest"

	if _, err := store.Pull(context.Background(), reference, t.TempDir()); err != nil {
		t.Fatalf("pull: %v", err)
	}

	manifests := fake.served["/v2/library/demo/manifests/latest"]
	if manifests < 2 {
		t.Fatalf("the manifest was fetched %d times across two pulls, want it asked for "+
			"every time: a tag moves, and a cache that answers first never notices",
			manifests)
	}
}

func TestACachedManifestWithoutItsBlobsIsNotUsed(t *testing.T) {
	root := t.TempDir()
	store, _, reference := pulledOnce(t, root)

	blobs, err := os.ReadDir(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatalf("read blobs: %v", err)
	}
	for _, blob := range blobs {
		if err := os.Remove(filepath.Join(root, "blobs", blob.Name())); err != nil {
			t.Fatalf("remove blob: %v", err)
		}
	}

	broken := unreachable(store)

	if _, err := store.Pull(context.Background(), reference, t.TempDir()); err == nil {
		t.Fatal("it reported an image ready whose layers are gone")
	}
	if broken.tried != 1 {
		t.Fatalf("it called the registry %d times, want 1: once the fallback cannot be "+
			"honoured the pull must stop, rather than announce it is using what the node "+
			"holds and then fail fetching a layer that is not there", broken.tried)
	}
}

func TestAnImageNeverPulledIsStillAFailure(t *testing.T) {
	store := New(t.TempDir(), logging.New("error", io.Discard))
	unreachable(store)

	_, err := store.Pull(context.Background(), "example.test/library/nothing:latest",
		t.TempDir())
	if err == nil {
		t.Fatal("a node reported an image ready that it has never seen")
	}
}
