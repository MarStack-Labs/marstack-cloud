//go:build linux

package container

import (
	"path/filepath"
	"strings"
)

type layout struct {
	root string
}

func (l layout) images() string {
	return filepath.Join(l.root, "images")
}

func (l layout) instance(id string) string {
	return filepath.Join(l.root, "instances", id)
}

func (l layout) rootfs(id string) string {
	return filepath.Join(l.instance(id), "rootfs")
}

func (l layout) pidFile(id string) string {
	return filepath.Join(l.instance(id), "pid")
}

func (l layout) logFile(id string) string {
	return filepath.Join(l.instance(id), "output.log")
}

func (l layout) cgroup(id string) string {
	return filepath.Join(cgroupRoot, cgroupSlice, id)
}

func imageFileName(image string) string {
	replaced := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(image)
	return strings.Trim(replaced, "._")
}
