//go:build linux

package qemu

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
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

func (r *Runtime) ExportVolume(volumeID, keyFile string) (io.ReadCloser, error) {
	source, err := r.volumeFile(volumeID)
	if err != nil {
		return nil, fmt.Errorf("read volume %s: %w", volumeID, err)
	}

	encrypted, err := isEncrypted(source)
	if err != nil {
		return nil, err
	}
	if encrypted && keyFile == "" {
		return nil, fmt.Errorf("volume %s is encrypted and no key was given, and copying it "+
			"without one would write its plaintext to this node", volumeID)
	}

	target, err := os.CreateTemp(r.volumeDir(), "export-*.qcow2")
	if err != nil {
		return nil, fmt.Errorf("create the export file: %w", err)
	}
	path := target.Name()
	target.Close()

	if err := r.copyOut(source, path, keyFile, encrypted); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("copy volume %s: %w", volumeID, err)
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

func isEncrypted(path string) (bool, error) {
	out, err := exec.Command("qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return false, fmt.Errorf("read the volume: %w", err)
	}

	var info struct {
		Encrypted bool `json:"encrypted"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return false, fmt.Errorf("decode the volume info: %w", err)
	}
	return info.Encrypted, nil
}

func (r *Runtime) copyOut(source, target, keyFile string, encrypted bool) error {
	if !encrypted {
		out, err := exec.Command("qemu-img", "convert", "-O", "qcow2", "-c",
			source, target).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	size, err := virtualSize(source, keyFile)
	if err != nil {
		return err
	}

	secret := "secret,id=vkey,file=" + keyFile
	create := exec.Command("qemu-img", "create", "--object", secret, "-f", "qcow2",
		"-o", "encrypt.format=luks,encrypt.key-secret=vkey", target, strconv.FormatInt(size, 10))
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("prepare the encrypted export: %w: %s",
			err, strings.TrimSpace(string(out)))
	}

	convert := exec.Command("qemu-img", "convert", "--object", secret,
		"--image-opts", "driver=qcow2,file.filename="+source+",encrypt.key-secret=vkey",
		"-n", "--target-image-opts",
		"driver=qcow2,file.filename="+target+",encrypt.key-secret=vkey")
	if out, err := convert.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func virtualSize(path, keyFile string) (int64, error) {
	args := append([]string{"info", "--output=json"}, imageArgs(path, keyFile)...)

	out, err := exec.Command("qemu-img", args...).Output()
	if err != nil {
		return 0, fmt.Errorf("read the volume: %w", err)
	}

	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return 0, fmt.Errorf("decode the volume info: %w", err)
	}
	if info.VirtualSize <= 0 {
		return 0, fmt.Errorf("the volume reports a virtual size of %d", info.VirtualSize)
	}
	return info.VirtualSize, nil
}
