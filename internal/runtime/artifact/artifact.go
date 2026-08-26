package artifact

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	fetchTimeout = 30 * time.Minute
	filePerm     = 0o644
	dirPerm      = 0o750
)

type Fetcher struct {
	dir    string
	log    *slog.Logger
	client *http.Client
}

func New(dir string, log *slog.Logger) *Fetcher {
	return &Fetcher{
		dir:    dir,
		log:    log,
		client: &http.Client{Timeout: fetchTimeout},
	}
}

func (f *Fetcher) Path(name string) string {
	return filepath.Join(f.dir, name)
}

func Existing(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("%s is empty", path)
	}
	return path, nil
}

func (f *Fetcher) Fetch(ctx context.Context, name, source, checksum string) (string, error) {
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		return "", fmt.Errorf("refusing to stage %q: a file name cannot contain a slash or two dots", name)
	}

	target := f.Path(name)
	if info, err := os.Stat(target); err == nil && info.Size() > 0 {
		return target, nil
	}

	if err := os.MkdirAll(f.dir, dirPerm); err != nil {
		return "", fmt.Errorf("create the image directory: %w", err)
	}

	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return "", fmt.Errorf("open the image directory: %w", err)
	}
	defer root.Close()

	f.log.Info("staging an image", "name", name, "source", source)

	temp := name + ".part"
	written, digest, err := f.download(ctx, root, temp, source, checksum)
	if err != nil {
		root.Remove(temp)
		return "", err
	}

	if checksum != "" && digest != checksum {
		root.Remove(temp)
		return "", fmt.Errorf("the download of %s has digest %s, but the catalog says %s",
			name, digest, checksum)
	}

	if err := root.Rename(temp, name); err != nil {
		return "", fmt.Errorf("finish staging %s: %w", name, err)
	}

	f.log.Info("image staged", "name", name, "bytes", written, "digest", digest)
	return target, nil
}

func (f *Fetcher) download(
	ctx context.Context, root *os.Root, temp, source, checksum string,
) (int64, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return 0, "", fmt.Errorf("build the download request: %w", err)
	}

	response, err := f.client.Do(request)
	if err != nil {
		return 0, "", fmt.Errorf("download %s: %w", source, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("download %s: the server answered %s", source, response.Status)
	}

	file, err := root.Create(temp)
	if err != nil {
		return 0, "", fmt.Errorf("open the staging file: %w", err)
	}
	defer file.Close()

	if err := file.Chmod(filePerm); err != nil {
		return 0, "", fmt.Errorf("set the staging file mode: %w", err)
	}

	digest, algorithm := hasherFor(checksum)
	written, err := io.Copy(io.MultiWriter(file, digest), response.Body)
	if err != nil {
		return 0, "", fmt.Errorf("write the staging file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return 0, "", fmt.Errorf("flush the staging file: %w", err)
	}

	return written, algorithm + ":" + hex.EncodeToString(digest.Sum(nil)), nil
}

func hasherFor(checksum string) (hash.Hash, string) {
	if strings.HasPrefix(checksum, "sha512:") {
		return sha512.New(), "sha512"
	}
	return sha256.New(), "sha256"
}
