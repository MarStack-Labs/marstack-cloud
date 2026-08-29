package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/s3"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

const KeyPrefix = "backups/"

type objectVault struct {
	client  *s3.Client
	staging string
	keys    *sealed.Keyring
}

func openObjectVault(dataDir string, client *s3.Client, keys *sealed.Keyring) (vault, error) {
	staging := filepath.Join(dataDir, DirName, "staging")
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return nil, fmt.Errorf("create the staging directory: %w", err)
	}

	entries, err := os.ReadDir(staging)
	if err == nil {
		for _, entry := range entries {
			os.Remove(filepath.Join(staging, entry.Name()))
		}
	}
	return &objectVault{client: client, staging: staging, keys: keys}, nil
}

func (v *objectVault) describe() string {
	return "bucket " + v.client.Bucket() + " on " + v.client.Endpoint()
}

func (v *objectVault) close() error {
	return nil
}

func keyFor(id string) string {
	return KeyPrefix + id
}

func (v *objectVault) write(
	ctx context.Context, id string, src io.Reader, limit int64,
) (written, error) {
	staged, err := os.CreateTemp(v.staging, id+"-*")
	if err != nil {
		return written{}, fmt.Errorf("open a staging file: %w", err)
	}
	path := staged.Name()
	defer os.Remove(path)

	if err := staged.Chmod(0o600); err != nil {
		staged.Close()
		return written{}, fmt.Errorf("restrict the staging file: %w", err)
	}

	result, err := pour(staged, src, limit, v.keys)
	if err != nil {
		staged.Close()
		return written{}, err
	}
	if err := staged.Close(); err != nil {
		return written{}, fmt.Errorf("close the staging file: %w", err)
	}

	body, err := os.Open(path)
	if err != nil {
		return written{}, fmt.Errorf("reopen the staging file: %w", err)
	}
	defer body.Close()

	info, err := body.Stat()
	if err != nil {
		return written{}, fmt.Errorf("measure the staging file: %w", err)
	}

	if err := v.client.Put(ctx, keyFor(id), body, info.Size()); err != nil {
		return written{}, err
	}
	return result, nil
}

func (v *objectVault) open(
	ctx context.Context, id, keyID, contentKey string,
) (io.ReadCloser, error) {
	body, err := v.client.Get(ctx, keyFor(id))
	if errors.Is(err, s3.ErrNotFound) {
		return nil, fmt.Errorf("the object store does not hold %s", id)
	}
	if err != nil {
		return nil, err
	}
	if contentKey == "" {
		return unseal(body, keyID, v.keys)
	}

	raw, err := v.unwrapContentKey(contentKey, keyID)
	if err != nil {
		body.Close()
		return nil, err
	}

	k, err := sealed.ParseKey(raw)
	if err != nil {
		body.Close()
		return nil, fmt.Errorf("read the content key: %w", err)
	}

	reader, err := sealed.Open(body, k)
	if err != nil {
		body.Close()
		return nil, fmt.Errorf("unseal the backup: %w", err)
	}
	return unsealed{Reader: reader, closer: body}, nil
}

func (v *objectVault) remove(ctx context.Context, id string) error {
	return v.client.Delete(ctx, keyFor(id))
}

type directVault interface {
	presign(method, id string, window time.Duration) (string, error)
	mintContentKey() (raw, wrapped, keyID string, err error)
	unwrapContentKey(wrapped, keyID string) (string, error)
}

func (v *objectVault) presign(method, id string, window time.Duration) (string, error) {
	return v.client.Presign(method, keyFor(id), window)
}

func (v *objectVault) mintContentKey() (string, string, string, error) {
	active, sealing := v.keys.Active()
	if !sealing {
		return "", "", "", nil
	}

	_, text := sealed.NewKey()

	wrapped, err := sealed.SealBytes([]byte(text), active)
	if err != nil {
		return "", "", "", fmt.Errorf("wrap a content key: %w", err)
	}
	return text, wrapped, v.keys.ActiveID(), nil
}

func (v *objectVault) unwrapContentKey(wrapped, keyID string) (string, error) {
	operator, held := v.keys.Find(keyID)
	if !held {
		return "", fmt.Errorf("this backup was keyed with %s, which this control plane "+
			"does not hold", keyID)
	}

	raw, err := sealed.OpenBytes(wrapped, operator)
	if err != nil {
		return "", fmt.Errorf("unwrap the content key: %w", err)
	}
	return string(raw), nil
}
