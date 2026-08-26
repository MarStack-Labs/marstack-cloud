package catalog

import (
	"context"
	"fmt"
	"log/slog"
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

	mu     sync.RWMutex
	images []Image
}

func New(dir string, log *slog.Logger) *Catalog {
	return &Catalog{fetcher: artifact.New(dir, log)}
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

	return c.fetcher.Fetch(ctx, FileName(in), in.Source, in.Checksum)
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
