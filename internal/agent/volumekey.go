package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const KeyRoot = "/run/marstack/keys"

func (a *Agent) volumeKeyFile(ctx context.Context, volumeID string) (string, error) {
	if !safeInstanceID(volumeID) {
		return "", fmt.Errorf("refusing a volume with id %q", volumeID)
	}

	path := filepath.Join(KeyRoot, volumeID)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	key, err := a.client.volumeKey(ctx, a.currentNodeID(), volumeID)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", fmt.Errorf("the control plane returned an empty key for %s", volumeID)
	}

	if err := os.MkdirAll(KeyRoot, 0o700); err != nil {
		return "", fmt.Errorf("create the key directory: %w", err)
	}

	root, err := os.OpenRoot(KeyRoot)
	if err != nil {
		return "", fmt.Errorf("open the key directory: %w", err)
	}
	defer root.Close()

	file, err := root.OpenFile(volumeID, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("write the volume key: %w", err)
	}
	if _, err := file.WriteString(key); err != nil {
		file.Close()
		return "", fmt.Errorf("write the volume key: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close the volume key: %w", err)
	}
	return path, nil
}
