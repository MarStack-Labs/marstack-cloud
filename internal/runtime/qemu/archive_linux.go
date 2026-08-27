//go:build linux

package qemu

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type exported struct {
	*os.File
	path string
}

func (e exported) Close() error {
	err := e.File.Close()
	os.Remove(e.path)
	return err
}

func (r *Runtime) HasVolume(volumeID string) bool {
	_, err := r.volumeFile(volumeID)
	return err == nil
}

func (r *Runtime) ExportVolume(volumeID string) (io.ReadCloser, error) {
	source, err := r.volumeFile(volumeID)
	if err != nil {
		return nil, fmt.Errorf("read volume %s: %w", volumeID, err)
	}

	target, err := os.CreateTemp(r.volumeDir(), "export-*.qcow2")
	if err != nil {
		return nil, fmt.Errorf("create the export file: %w", err)
	}
	path := target.Name()
	target.Close()

	convert := exec.Command("qemu-img", "convert", "-O", "qcow2", "-c", source, path)
	if out, err := convert.CombinedOutput(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("copy volume %s: %w: %s",
			volumeID, err, strings.TrimSpace(string(out)))
	}

	file, err := os.Open(path)
	if err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("open the export file: %w", err)
	}
	return exported{File: file, path: path}, nil
}

func (r *Runtime) ImportVolume(volumeID string, content io.Reader) error {
	if strings.ContainsAny(volumeID, "/.") {
		return fmt.Errorf("refusing a volume with id %q", volumeID)
	}
	if err := os.MkdirAll(r.volumeDir(), 0o750); err != nil {
		return fmt.Errorf("create the volume directory: %w", err)
	}

	root, err := os.OpenRoot(r.volumeDir())
	if err != nil {
		return fmt.Errorf("open the volume directory: %w", err)
	}
	defer root.Close()

	partial := volumeID + ".part"
	file, err := root.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create the volume file: %w", err)
	}

	if _, err := io.Copy(file, content); err != nil {
		file.Close()
		root.Remove(partial)
		return fmt.Errorf("write the volume file: %w", err)
	}
	if err := file.Close(); err != nil {
		root.Remove(partial)
		return fmt.Errorf("close the volume file: %w", err)
	}

	if err := root.Rename(partial, volumeID+".qcow2"); err != nil {
		root.Remove(partial)
		return fmt.Errorf("place the volume file: %w", err)
	}
	return nil
}
