package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

const DirName = "backups"

type Keyring struct {
	active *sealed.Key
	byID   map[string]sealed.Key
}

func NewKeyring(keys []sealed.Key) *Keyring {
	ring := &Keyring{byID: make(map[string]sealed.Key, len(keys))}
	for i, k := range keys {
		ring.byID[k.ID()] = k
		if i == 0 {
			first := k
			ring.active = &first
		}
	}
	return ring
}

func (r *Keyring) Sealing() bool {
	return r != nil && r.active != nil
}

func (r *Keyring) ActiveID() string {
	if !r.Sealing() {
		return ""
	}
	return r.active.ID()
}

func (r *Keyring) find(id string) (sealed.Key, bool) {
	if r == nil {
		return sealed.Key{}, false
	}
	k, ok := r.byID[id]
	return k, ok
}

type vault struct {
	root *os.Root
	keys *Keyring
}

func openVault(dataDir string, keys *Keyring) (*vault, error) {
	path := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(path, 0o750); err != nil {
		return nil, fmt.Errorf("create the backup directory: %w", err)
	}

	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open the backup directory: %w", err)
	}
	return &vault{root: root, keys: keys}, nil
}

func (v *vault) close() error {
	return v.root.Close()
}

type written struct {
	Size     int64
	Checksum string
	KeyID    string
}

func (v *vault) write(id string, src io.Reader, limit int64) (written, error) {
	partial := id + ".part"

	file, err := v.root.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return written{}, fmt.Errorf("open the backup file: %w", err)
	}

	result, err := v.pour(file, src, limit)
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

func (v *vault) pour(file io.Writer, src io.Reader, limit int64) (written, error) {
	digest := sha256.New()
	bounded := io.LimitReader(src, limit+1)

	var (
		size int64
		err  error
	)
	if v.keys.Sealing() {
		size, err = sealed.Seal(file, io.TeeReader(bounded, digest), *v.keys.active)
	} else {
		size, err = io.Copy(io.MultiWriter(file, digest), bounded)
	}
	if err != nil {
		return written{}, fmt.Errorf("write the backup: %w", err)
	}
	if size > limit {
		return written{}, fmt.Errorf("the backup is larger than %d bytes", limit)
	}

	return written{
		Size:     size,
		Checksum: hex.EncodeToString(digest.Sum(nil)),
		KeyID:    v.keys.ActiveID(),
	}, nil
}

func (v *vault) open(id, keyID string) (io.ReadCloser, error) {
	file, err := v.root.Open(id)
	if err != nil {
		return nil, fmt.Errorf("open the backup: %w", err)
	}

	if keyID == "" {
		return file, nil
	}

	k, known := v.keys.find(keyID)
	if !known {
		file.Close()
		return nil, fmt.Errorf("this backup was sealed with key %s, which this control plane "+
			"does not hold", keyID)
	}

	reader, err := sealed.Open(file, k)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("unseal the backup: %w", err)
	}
	return unsealed{Reader: reader, file: file}, nil
}

type unsealed struct {
	io.Reader
	file *os.File
}

func (u unsealed) Close() error {
	return u.file.Close()
}

func (v *vault) remove(id string) error {
	if err := v.root.Remove(id); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the backup: %w", err)
	}
	return nil
}
