package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const DirName = "backups"

type vault struct {
	root *os.Root
}

func openVault(dataDir string) (*vault, error) {
	path := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(path, 0o750); err != nil {
		return nil, fmt.Errorf("create the backup directory: %w", err)
	}

	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open the backup directory: %w", err)
	}
	return &vault{root: root}, nil
}

func (v *vault) close() error {
	return v.root.Close()
}

func (v *vault) write(id string, src io.Reader, limit int64) (int64, string, error) {
	partial := id + ".part"

	file, err := v.root.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", fmt.Errorf("open the backup file: %w", err)
	}

	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(src, limit+1))
	if err != nil {
		file.Close()
		v.root.Remove(partial)
		return 0, "", fmt.Errorf("write the backup: %w", err)
	}
	if err := file.Close(); err != nil {
		v.root.Remove(partial)
		return 0, "", fmt.Errorf("close the backup: %w", err)
	}
	if written > limit {
		v.root.Remove(partial)
		return 0, "", fmt.Errorf("the backup is larger than %d bytes", limit)
	}

	if err := v.root.Rename(partial, id); err != nil {
		v.root.Remove(partial)
		return 0, "", fmt.Errorf("place the backup: %w", err)
	}
	return written, hex.EncodeToString(digest.Sum(nil)), nil
}

func (v *vault) open(id string) (*os.File, int64, error) {
	file, err := v.root.Open(id)
	if err != nil {
		return nil, 0, fmt.Errorf("open the backup: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, fmt.Errorf("size the backup: %w", err)
	}
	return file, info.Size(), nil
}

func (v *vault) remove(id string) error {
	if err := v.root.Remove(id); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the backup: %w", err)
	}
	return nil
}
