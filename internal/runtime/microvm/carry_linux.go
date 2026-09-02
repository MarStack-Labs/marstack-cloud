//go:build linux

package microvm

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (r *Runtime) HasDisk(instanceID string) bool {
	_, err := os.Stat(r.rootfsFile(instanceID))
	return err == nil
}

func (r *Runtime) ExportDisk(instanceID string) (io.ReadCloser, error) {
	file, err := os.Open(r.rootfsFile(instanceID))
	if err != nil {
		return nil, fmt.Errorf("open the root filesystem of %s: %w", instanceID, err)
	}
	return file, nil
}

func (r *Runtime) ImportDisk(instanceID string, content io.Reader) error {
	dir := r.instanceDir(instanceID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create the instance directory: %w", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open the instance directory: %w", err)
	}
	defer root.Close()

	name := filepath.Base(r.rootfsFile(instanceID))
	partial := name + ".part"

	file, err := root.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create the carried root filesystem: %w", err)
	}
	if _, err := io.Copy(file, content); err != nil {
		file.Close()
		_ = root.Remove(partial)
		return fmt.Errorf("write the carried root filesystem: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(partial)
		return fmt.Errorf("close the carried root filesystem: %w", err)
	}
	if err := root.Rename(partial, name); err != nil {
		_ = root.Remove(partial)
		return fmt.Errorf("place the carried root filesystem: %w", err)
	}
	return nil
}

func (r *Runtime) Forget(instanceID string) error {
	if err := os.RemoveAll(r.instanceDir(instanceID)); err != nil {
		return fmt.Errorf("remove the instance directory: %w", err)
	}
	return nil
}
