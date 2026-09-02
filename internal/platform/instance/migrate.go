package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

const (
	MaxParkedDiskBytes = 64 << 30
	parkedDir          = "migrations"
)

func (s *service) parkDir() (string, error) {
	if s.dataDir == "" {
		return "", errors.New("this control plane has nowhere to park a disk")
	}

	dir := filepath.Join(s.dataDir, parkedDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the migration directory: %w", err)
	}
	return dir, nil
}

func parkedName(instanceID string) (string, error) {
	if instanceID == "" || strings.ContainsAny(instanceID, "/.") {
		return "", fmt.Errorf("refusing to park a disk for %q", instanceID)
	}
	return instanceID + ".qcow2", nil
}

func (s *service) beginMigration(ctx context.Context, id, nodeID string) error {
	in, err := s.repo.get(ctx, id)
	if err != nil {
		return translate(err)
	}
	if in.NodeID != nodeID {
		return fault.Conflict("instance_moved",
			"the instance is no longer on the node it was being moved off")
	}
	if in.Migrating {
		return nil
	}

	if err := s.repo.setDesired(ctx, id, DesiredStopped, s.now()); err != nil {
		return translate(err)
	}
	return s.repo.setMigrating(ctx, id, true, false, "", s.now())
}

func (s *service) parkDisk(ctx context.Context, nodeID, id string, content io.Reader) error {
	in, err := s.repo.get(ctx, id)
	if err != nil {
		return translate(err)
	}
	if in.NodeID != nodeID {
		return fault.NotFound("instance_not_on_node",
			"no instance with that id is assigned to this node")
	}
	if !in.Migrating {
		return fault.Conflict("not_migrating",
			"this instance is not being moved, so there is nowhere for a disk to go")
	}

	dir, err := s.parkDir()
	if err != nil {
		return fault.Internal(err)
	}
	name, err := parkedName(id)
	if err != nil {
		return fault.Invalid("invalid_instance", err.Error())
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fault.Internal(fmt.Errorf("open the migration directory: %w", err))
	}
	defer root.Close()

	partial := name + ".part"
	file, err := root.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fault.Internal(fmt.Errorf("create the parked disk: %w", err))
	}

	written, err := io.Copy(file, io.LimitReader(content, MaxParkedDiskBytes+1))
	if err != nil {
		file.Close()
		_ = root.Remove(partial)
		return fault.Internal(fmt.Errorf("write the parked disk: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(partial)
		return fault.Internal(fmt.Errorf("close the parked disk: %w", err))
	}
	if written > MaxParkedDiskBytes {
		_ = root.Remove(partial)
		return fault.Invalid("disk_too_large", fmt.Sprintf(
			"a disk being moved must be at most %d bytes", MaxParkedDiskBytes))
	}

	if err := root.Rename(partial, name); err != nil {
		_ = root.Remove(partial)
		return fault.Internal(fmt.Errorf("place the parked disk: %w", err))
	}

	return s.repo.setMigrating(ctx, id, true, true, nodeID, s.now())
}

func (s *service) parkedDisk(ctx context.Context, nodeID, id string) (*os.File, error) {
	in, err := s.repo.get(ctx, id)
	if err != nil {
		return nil, translate(err)
	}
	if in.NodeID != nodeID {
		return nil, fault.NotFound("instance_not_on_node",
			"no instance with that id is assigned to this node")
	}
	if !in.Migrating || !in.DiskParked {
		return nil, fault.NotFound("no_parked_disk",
			"there is no disk waiting for this instance")
	}
	if nodeID == in.DiskFrom {
		return nil, fault.Conflict("disk_came_from_here",
			"this is the node that handed the disk over, and taking it back would end the "+
				"move where it started")
	}

	dir, err := s.parkDir()
	if err != nil {
		return nil, fault.Internal(err)
	}
	name, err := parkedName(id)
	if err != nil {
		return nil, fault.Invalid("invalid_instance", err.Error())
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fault.Internal(fmt.Errorf("open the migration directory: %w", err))
	}
	defer root.Close()

	file, err := root.Open(name)
	if err != nil {
		return nil, fault.NotFound("no_parked_disk",
			"the disk that was waiting for this instance is gone")
	}
	return file, nil
}

func (s *service) finishMigration(ctx context.Context, nodeID, id string) (Instance, error) {
	in, err := s.repo.get(ctx, id)
	if err != nil {
		return Instance{}, translate(err)
	}
	if in.NodeID != nodeID {
		return Instance{}, fault.NotFound("instance_not_on_node",
			"no instance with that id is assigned to this node")
	}
	if !in.Migrating {
		return in, nil
	}

	if err := s.repo.setMigrating(ctx, id, false, false, "", s.now()); err != nil {
		return Instance{}, translate(err)
	}
	if err := s.repo.setDesired(ctx, id, DesiredRunning, s.now()); err != nil {
		return Instance{}, translate(err)
	}
	s.forgetParked(id)

	return s.get(ctx, id)
}

func (s *service) forgetParked(id string) {
	dir, err := s.parkDir()
	if err != nil {
		return
	}
	name, err := parkedName(id)
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(dir, name))
}
