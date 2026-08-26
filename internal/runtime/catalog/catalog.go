package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/marstack-labs/marstack-cloud/internal/runtime/artifact"
)

const (
	KindDisk   = "disk"
	KindISO    = "iso"
	KindKernel = "kernel"
)

type Image struct {
	ID       string
	Name     string
	Kind     string
	Arch     string
	Source   string
	Checksum string
}

type Catalog struct {
	fetcher *artifact.Fetcher
	log     *slog.Logger

	mu     sync.RWMutex
	images []Image
}

func New(dir string, log *slog.Logger) *Catalog {
	return &Catalog{fetcher: artifact.New(dir, log), log: log}
}

func (c *Catalog) Replace(images []Image) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.images = images
}

func (c *Catalog) lookup(reference string) (Image, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, in := range c.images {
		if in.Name == reference || in.ID == reference {
			return in, true
		}
	}
	return Image{}, false
}

func (c *Catalog) Stage(ctx context.Context, reference, kind string) (string, error) {
	in, known := c.lookup(reference)
	if !known {
		local := c.fetcher.Path(FileName(Image{Name: reference, Kind: kind}))
		if staged, err := artifact.Existing(local); err == nil {
			return staged, nil
		}
		return "", fmt.Errorf(
			"no image named %s is registered, and none is staged on this node: register it with "+
				"marstack image create --name %s --kind %s --source <url>", reference, reference, kind)
	}
	if in.Kind != kind {
		return "", fmt.Errorf("image %s is kind %s, but this needs kind %s", reference, in.Kind, kind)
	}

	name := FileName(in)
	path, err := c.fetcher.Fetch(ctx, name, in.Source, in.Checksum)
	if err != nil {
		return "", err
	}

	if err := c.fetcher.MarkOrigin(name, in.ID); err != nil {
		c.log.Warn("staged an image without recording where it came from",
			"name", name, "error", err)
	}
	return path, nil
}

func (c *Catalog) Staged() ([]artifact.Staged, error) {
	return c.fetcher.Staged()
}

func (c *Catalog) Prune(inUse []string) error {
	staged, err := c.fetcher.Staged()
	if err != nil {
		return err
	}

	keep := make(map[string]bool, len(inUse))
	for _, reference := range inUse {
		keep[reference] = true
	}

	c.mu.RLock()
	known := make(map[string]bool, len(c.images))
	for _, in := range c.images {
		known[in.ID] = true
	}
	c.mu.RUnlock()

	for _, file := range staged {
		if known[file.Origin] || keep[file.Origin] || keep[nameOf(file.Name)] {
			continue
		}

		c.log.Info("removing an image no longer in the catalog",
			"name", file.Name, "image", file.Origin, "bytes", file.Bytes)
		if err := c.fetcher.Discard(file.Name); err != nil {
			return err
		}
	}
	return nil
}

func nameOf(fileName string) string {
	for _, suffix := range []string{".qcow2", ".iso", ".Image"} {
		if strings.HasSuffix(fileName, suffix) {
			return strings.TrimSuffix(fileName, suffix)
		}
	}
	return fileName
}

func FileName(in Image) string {
	switch in.Kind {
	case KindISO:
		return in.Name + ".iso"
	case KindKernel:
		return in.Name + ".Image"
	default:
		return in.Name + ".qcow2"
	}
}
