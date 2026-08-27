package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Volumes interface {
	Source(ctx context.Context, volumeID, projectID string) (Source, error)
}

type clock func() time.Time

type service struct {
	repo    *repository
	vault   *vault
	volumes Volumes
	now     clock
}

func newService(repo *repository, vault *vault, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, vault: vault, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Backup, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Backup{}, err
	}
	if s.volumes == nil {
		return Backup{}, fault.Unavailable("volumes_unavailable",
			"the platform cannot look up which node holds the volume")
	}

	source, err := s.volumes.Source(ctx, params.VolumeID, params.ProjectID)
	if err != nil {
		return Backup{}, err
	}
	if source.NodeID == "" {
		return Backup{}, fault.Conflict("volume_empty",
			"the volume has never been attached, so no node holds anything to copy")
	}

	now := s.now()
	b := Backup{
		ID:        ids.New("bkp"),
		ProjectID: params.ProjectID,
		VolumeID:  params.VolumeID,
		NodeID:    source.NodeID,
		Name:      params.Name,
		State:     StatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.insert(ctx, b); err != nil {
		return Backup{}, translate(err)
	}
	return b, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Backup, error) {
	backups, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return backups, nil
}

func (s *service) listForVolume(ctx context.Context, volumeID, projectID string) ([]Backup, error) {
	if s.volumes == nil {
		return nil, fault.Unavailable("volumes_unavailable",
			"the platform cannot look up which node holds the volume")
	}
	if _, err := s.volumes.Source(ctx, volumeID, projectID); err != nil {
		return nil, err
	}

	backups, err := s.repo.listForVolume(ctx, volumeID)
	if err != nil {
		return nil, translate(err)
	}
	return backups, nil
}

func (s *service) getIn(ctx context.Context, id, projectID string) (Backup, error) {
	b, err := s.repo.byID(ctx, id)
	if err != nil {
		return Backup{}, translate(err)
	}
	if b.ProjectID != projectID {
		return Backup{}, fault.NotFound("backup_not_found", "no backup with that id exists")
	}
	return b, nil
}

func (s *service) pendingOn(ctx context.Context, nodeID string) ([]Backup, error) {
	backups, err := s.repo.pendingOn(ctx, nodeID)
	if err != nil {
		return nil, translate(err)
	}
	return backups, nil
}

func (s *service) store(ctx context.Context, id, nodeID string, content io.Reader) (Backup, error) {
	b, err := s.repo.byID(ctx, id)
	if err != nil {
		return Backup{}, translate(err)
	}
	if b.NodeID != nodeID {
		return Backup{}, fault.NotFound("backup_not_found", "no backup with that id exists")
	}
	if b.State == StateReady {
		return b, nil
	}

	size, checksum, err := s.vault.write(b.ID, content, MaxBytes)
	if err != nil {
		if markErr := s.repo.mark(ctx, b.ID, StateFailed, err.Error(), 0, "", s.now()); markErr != nil {
			return Backup{}, fault.Internal(markErr)
		}
		return Backup{}, fault.Internal(err)
	}

	at := s.now()
	if err := s.repo.mark(ctx, b.ID, StateReady, "", size, checksum, at); err != nil {
		return Backup{}, translate(err)
	}

	b.State = StateReady
	b.SizeBytes = size
	b.Checksum = checksum
	b.UpdatedAt = at
	return b, nil
}

func (s *service) fail(ctx context.Context, id, nodeID, message string) error {
	b, err := s.repo.byID(ctx, id)
	if err != nil {
		return translate(err)
	}
	if b.NodeID != nodeID {
		return fault.NotFound("backup_not_found", "no backup with that id exists")
	}
	if message == "" {
		message = "the node could not copy the volume"
	}
	return translate(s.repo.mark(ctx, b.ID, StateFailed, message, 0, "", s.now()))
}

func (s *service) content(ctx context.Context, id string) (*os.File, int64, error) {
	b, err := s.repo.byID(ctx, id)
	if err != nil {
		return nil, 0, translate(err)
	}
	if b.State != StateReady {
		return nil, 0, fault.Conflict("backup_not_ready",
			"the backup is "+b.State+" and holds no bytes yet")
	}

	file, size, err := s.vault.open(b.ID)
	if err != nil {
		return nil, 0, fault.Internal(err)
	}
	return file, size, nil
}

func (s *service) restorable(ctx context.Context, id, projectID string) (int64, error) {
	b, err := s.getIn(ctx, id, projectID)
	if err != nil {
		return 0, err
	}
	if b.State != StateReady {
		return 0, fault.Conflict("backup_not_ready",
			"the backup is "+b.State+" and cannot be restored yet")
	}
	return b.SizeBytes, nil
}

func (s *service) remove(ctx context.Context, id, projectID string) error {
	b, err := s.getIn(ctx, id, projectID)
	if err != nil {
		return err
	}
	if err := s.vault.remove(b.ID); err != nil {
		return fault.Internal(err)
	}
	return translate(s.repo.delete(ctx, b.ID))
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("backup_not_found", "no backup with that id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("backup_name_taken",
			"a backup of that volume already carries that name")
	default:
		return fault.Internal(err)
	}
}
