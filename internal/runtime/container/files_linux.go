//go:build linux

package container

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func dropFiles(rootfs string, drops []workload.FileDrop) error {
	if len(drops) == 0 {
		return nil
	}

	root, err := os.OpenRoot(rootfs)
	if err != nil {
		return fmt.Errorf("open the rootfs: %w", err)
	}
	defer root.Close()

	for _, drop := range drops {
		inside := filepath.Clean(drop.Path)[1:]

		if dir := filepath.Dir(inside); dir != "." {
			if err := mkdirAllIn(root, dir); err != nil {
				return err
			}
		}

		file, err := root.OpenFile(inside, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create %s in the rootfs: %w", drop.Path, err)
		}

		_, writeErr := file.Write(drop.Content)
		chmodErr := file.Chmod(os.FileMode(drop.Mode))
		closeErr := file.Close()

		switch {
		case writeErr != nil:
			return fmt.Errorf("write %s: %w", drop.Path, writeErr)
		case chmodErr != nil:
			return fmt.Errorf("set the mode of %s: %w", drop.Path, chmodErr)
		case closeErr != nil:
			return fmt.Errorf("close %s: %w", drop.Path, closeErr)
		}
	}
	return nil
}

func mkdirAllIn(root *os.Root, dir string) error {
	built := ""
	for _, part := range splitPath(dir) {
		built = filepath.Join(built, part)
		if err := root.Mkdir(built, 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create %s in the rootfs: %w", built, err)
		}
	}
	return nil
}

func splitPath(dir string) []string {
	parts := make([]string, 0, 8)
	for dir != "." && dir != string(filepath.Separator) && dir != "" {
		parts = append([]string{filepath.Base(dir)}, parts...)
		dir = filepath.Dir(dir)
	}
	return parts
}
