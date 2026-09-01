package microvm

import (
	"os"
	"path/filepath"

	"github.com/marstack-labs/marstack-cloud/internal/runtime/console"
)

func (r *Runtime) LogPath(instanceID string) (string, bool) {
	path := filepath.Join(r.instanceDir(instanceID), console.LogName)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}
