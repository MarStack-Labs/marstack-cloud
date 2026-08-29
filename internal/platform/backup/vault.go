package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

const DirName = "backups"

type written struct {
	Size     int64
	Checksum string
	KeyID    string
}

type vault interface {
	write(ctx context.Context, id string, src io.Reader, limit int64) (written, error)
	open(ctx context.Context, id, keyID, contentKey string) (io.ReadCloser, error)
	remove(ctx context.Context, id string) error
	close() error
	describe() string
}

func pour(dst io.Writer, src io.Reader, limit int64, keys *sealed.Keyring) (written, error) {
	var (
		measured sealed.Measured
		err      error
	)
	if active, ok := keys.Active(); ok {
		measured, err = sealed.SealMeasured(dst, src, active, limit)
	} else {
		measured, err = sealed.CopyMeasured(dst, src, limit)
	}
	if err != nil {
		return written{}, err
	}

	return written{
		Size:     measured.Size,
		Checksum: measured.Checksum,
		KeyID:    keys.ActiveID(),
	}, nil
}

func unseal(body io.ReadCloser, keyID string, keys *sealed.Keyring) (io.ReadCloser, error) {
	if keyID == "" {
		return body, nil
	}

	k, known := keys.Find(keyID)
	if !known {
		body.Close()
		return nil, fmt.Errorf("this backup was sealed with key %s, which this control plane "+
			"does not hold", keyID)
	}

	reader, err := sealed.Open(body, k)
	if err != nil {
		body.Close()
		return nil, fmt.Errorf("unseal the backup: %w", err)
	}
	return unsealed{Reader: reader, closer: body}, nil
}

type unsealed struct {
	io.Reader
	closer io.Closer
}

func (u unsealed) Close() error {
	return u.closer.Close()
}

type diskVault struct {
	root *os.Root
	path string
	keys *sealed.Keyring
}

func openVault(dataDir string, keys *sealed.Keyring) (vault, error) {
	path := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(path, 0o750); err != nil {
		return nil, fmt.Errorf("create the backup directory: %w", err)
	}

	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open the backup directory: %w", err)
	}
	return &diskVault{root: root, path: path, keys: keys}, nil
}

func (v *diskVault) describe() string {
	return "the control plane disk at " + v.path
}

func (v *diskVault) close() error {
	return v.root.Close()
}

func (v *diskVault) write(_ context.Context, id string, src io.Reader, limit int64) (written, error) {
	partial := id + ".part"

	file, err := v.root.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return written{}, fmt.Errorf("open the backup file: %w", err)
	}

	result, err := pour(file, src, limit, v.keys)
	closeErr := file.Close()

	if err == nil && closeErr != nil {
		err = fmt.Errorf("close the backup: %w", closeErr)
	}
	if err != nil {
		v.root.Remove(partial)
		return written{}, err
	}

	if err := v.root.Rename(partial, id); err != nil {
		v.root.Remove(partial)
		return written{}, fmt.Errorf("place the backup: %w", err)
	}
	return result, nil
}

func (v *diskVault) open(_ context.Context, id, keyID, _ string) (io.ReadCloser, error) {
	file, err := v.root.Open(id)
	if err != nil {
		return nil, fmt.Errorf("open the backup: %w", err)
	}
	return unseal(file, keyID, v.keys)
}

func (v *diskVault) remove(_ context.Context, id string) error {
	if err := v.root.Remove(id); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the backup: %w", err)
	}
	return nil
}
