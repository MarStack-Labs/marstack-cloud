package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const whiteoutPrefix = ".wh."

func extractLayer(reader io.Reader, dest string) error {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return fmt.Errorf("open root filesystem: %w", err)
	}
	defer root.Close()

	decompressed, closer, err := decompress(reader)
	if err != nil {
		return err
	}
	if closer != nil {
		defer closer.Close()
	}

	archive := tar.NewReader(decompressed)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read layer: %w", err)
		}

		name := path.Clean("/" + header.Name)
		if name == "/" {
			continue
		}
		name = strings.TrimPrefix(name, "/")

		if applied, err := applyWhiteout(root, name); err != nil {
			return err
		} else if applied {
			continue
		}

		if err := writeEntry(root, archive, header, name); err != nil {
			return err
		}
	}
}

func decompress(reader io.Reader) (io.Reader, io.Closer, error) {
	header := make([]byte, 2)
	read, err := io.ReadFull(reader, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("peek layer: %w", err)
	}

	joined := io.MultiReader(bytes.NewReader(header[:read]), reader)
	if read == 2 && header[0] == 0x1f && header[1] == 0x8b {
		unzipped, err := gzip.NewReader(joined)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip layer: %w", err)
		}
		return unzipped, unzipped, nil
	}
	return joined, nil, nil
}

func applyWhiteout(root *os.Root, name string) (bool, error) {
	dir, base := path.Split(name)
	if !strings.HasPrefix(base, whiteoutPrefix) {
		return false, nil
	}

	if base == whiteoutPrefix+whiteoutPrefix+"opq" {
		return true, clearDirectory(root, path.Clean(dir))
	}

	target := path.Join(dir, strings.TrimPrefix(base, whiteoutPrefix))
	if err := root.RemoveAll(target); err != nil {
		return true, fmt.Errorf("apply whiteout to %s: %w", target, err)
	}
	return true, nil
}

func clearDirectory(root *os.Root, dir string) error {
	handle, err := root.Open(dir)
	if err != nil {
		return nil
	}

	entries, err := handle.ReadDir(-1)
	handle.Close()
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if err := root.RemoveAll(path.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("apply opaque whiteout in %s: %w", dir, err)
		}
	}
	return nil
}

func writeEntry(root *os.Root, archive *tar.Reader, header *tar.Header, name string) error {
	mode := header.FileInfo().Mode().Perm()

	switch header.Typeflag {
	case tar.TypeDir:
		return mkdirAll(root, name, mode)

	case tar.TypeReg:
		if err := mkdirAll(root, path.Dir(name), 0o755); err != nil {
			return err
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		defer file.Close()

		if _, err := io.CopyN(file, archive, header.Size); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("write %s: %w", name, err)
		}
		return nil

	case tar.TypeSymlink:
		if err := mkdirAll(root, path.Dir(name), 0o755); err != nil {
			return err
		}
		_ = root.Remove(name)
		if err := root.Symlink(header.Linkname, name); err != nil {
			return fmt.Errorf("link %s: %w", name, err)
		}
		return nil

	case tar.TypeLink:
		if err := mkdirAll(root, path.Dir(name), 0o755); err != nil {
			return err
		}
		_ = root.Remove(name)
		target := strings.TrimPrefix(path.Clean("/"+header.Linkname), "/")
		if err := root.Link(target, name); err != nil {
			return fmt.Errorf("hard link %s: %w", name, err)
		}
		return nil

	default:
		return nil
	}
}

func mkdirAll(root *os.Root, name string, mode os.FileMode) error {
	if name == "." || name == "/" || name == "" {
		return nil
	}
	if err := root.MkdirAll(name, mode); err != nil {
		return fmt.Errorf("create directory %s: %w", name, err)
	}
	return nil
}
