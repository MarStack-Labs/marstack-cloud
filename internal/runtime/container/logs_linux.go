package container

import "os"

func (r *Runtime) LogPath(instanceID string) (string, bool) {
	path := r.layout.logFile(instanceID)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}
