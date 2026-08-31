package guest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func Drop(root string, drops []workload.FileDrop) error {
	if len(drops) == 0 {
		return nil
	}

	confined, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open the rootfs: %w", err)
	}
	defer confined.Close()

	for _, drop := range drops {
		if err := write(confined, drop); err != nil {
			return err
		}
	}
	return nil
}

func write(confined *os.Root, drop workload.FileDrop) error {
	inside := filepath.Clean(drop.Path)[1:]

	if dir := filepath.Dir(inside); dir != "." {
		if err := mkdirAll(confined, dir); err != nil {
			return err
		}
	}

	file, err := confined.OpenFile(inside, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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
	return nil
}

func mkdirAll(confined *os.Root, dir string) error {
	built := ""
	for _, part := range split(dir) {
		built = filepath.Join(built, part)
		if err := confined.Mkdir(built, 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create %s in the rootfs: %w", built, err)
		}
	}
	return nil
}

func split(dir string) []string {
	parts := make([]string, 0, 8)
	for dir != "." && dir != string(filepath.Separator) && dir != "" {
		parts = append([]string{filepath.Base(dir)}, parts...)
		dir = filepath.Dir(dir)
	}
	return parts
}
