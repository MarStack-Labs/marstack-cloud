package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	Env        []string `json:"env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

func (c Config) Command() []string {
	command := make([]string, 0, len(c.Entrypoint)+len(c.Cmd))
	command = append(command, c.Entrypoint...)
	return append(command, c.Cmd...)
}

type Store struct {
	root     string
	log      *slog.Logger
	registry *registry
}

func New(root string, log *slog.Logger) *Store {
	return &Store{
		root:     root,
		log:      log,
		registry: newRegistry(false),
	}
}

func (s *Store) blobDir() string {
	return filepath.Join(s.root, "blobs")
}

func (s *Store) blobPath(digest string) string {
	return filepath.Join(s.blobDir(), strings.ReplaceAll(digest, ":", "_"))
}

func (s *Store) openBlob(digest string) (*os.File, error) {
	if err := validateDigest(digest); err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(s.blobDir())
	if err != nil {
		return nil, fmt.Errorf("open blob directory: %w", err)
	}
	defer root.Close()

	return root.Open(filepath.Base(s.blobPath(digest)))
}

func (s *Store) Pull(ctx context.Context, reference, dest string) (Config, error) {
	ref, err := ParseReference(reference)
	if err != nil {
		return Config{}, err
	}

	s.log.Info("pulling image", "image", ref.String())

	parsed, err := s.registry.manifest(ctx, ref)
	if err != nil {
		return Config{}, err
	}
	if len(parsed.Layers) == 0 {
		return Config{}, fmt.Errorf("image %s has no layers", ref)
	}

	for index, layer := range parsed.Layers {
		if err := s.fetchBlob(ctx, ref, layer); err != nil {
			return Config{}, err
		}

		file, err := s.openBlob(layer.Digest)
		if err != nil {
			return Config{}, fmt.Errorf("open cached layer: %w", err)
		}

		err = extractLayer(file, dest)
		file.Close()

		if err != nil {
			return Config{}, fmt.Errorf("apply layer %d of %s: %w", index+1, ref, err)
		}
	}

	config, err := s.imageConfig(ctx, ref, parsed.Config)
	if err != nil {
		return Config{}, err
	}

	s.log.Info("image ready",
		"image", ref.String(),
		"layers", len(parsed.Layers),
		"command", config.Command(),
	)
	return config, nil
}

func (s *Store) imageConfig(ctx context.Context, ref Reference, blob descriptor) (Config, error) {
	if blob.Digest == "" {
		return Config{}, nil
	}
	if err := s.fetchBlob(ctx, ref, blob); err != nil {
		return Config{}, err
	}

	file, err := s.openBlob(blob.Digest)
	if err != nil {
		return Config{}, fmt.Errorf("open image config: %w", err)
	}
	defer file.Close()

	var document struct {
		Config struct {
			Entrypoint []string `json:"Entrypoint"`
			Cmd        []string `json:"Cmd"`
			Env        []string `json:"Env"`
			WorkingDir string   `json:"WorkingDir"`
		} `json:"config"`
	}
	if err := json.NewDecoder(io.LimitReader(file, maxManifest)).Decode(&document); err != nil {
		return Config{}, fmt.Errorf("decode image config: %w", err)
	}

	return Config{
		Entrypoint: document.Config.Entrypoint,
		Cmd:        document.Config.Cmd,
		Env:        document.Config.Env,
		WorkingDir: document.Config.WorkingDir,
	}, nil
}

func (s *Store) fetchBlob(ctx context.Context, ref Reference, layer descriptor) error {
	if err := validateDigest(layer.Digest); err != nil {
		return err
	}
	if err := os.MkdirAll(s.blobDir(), 0o750); err != nil {
		return fmt.Errorf("create blob directory: %w", err)
	}

	root, err := os.OpenRoot(s.blobDir())
	if err != nil {
		return fmt.Errorf("open blob directory: %w", err)
	}
	defer root.Close()

	name := filepath.Base(s.blobPath(layer.Digest))
	if _, err := root.Stat(name); err == nil {
		return nil
	}

	body, err := s.registry.blob(ctx, ref, layer.Digest)
	if err != nil {
		return err
	}
	defer body.Close()

	data, err := io.ReadAll(io.LimitReader(body, maxLayer))
	if err != nil {
		return fmt.Errorf("download layer: %w", err)
	}
	if err := verifyDigest(data, layer.Digest); err != nil {
		return err
	}

	partial := name + ".partial"
	if err := root.WriteFile(partial, data, 0o640); err != nil {
		return fmt.Errorf("write layer: %w", err)
	}
	if err := root.Rename(partial, name); err != nil {
		return fmt.Errorf("commit layer: %w", err)
	}

	s.log.Debug("layer cached", "digest", layer.Digest, "bytes", len(data))
	return nil
}
