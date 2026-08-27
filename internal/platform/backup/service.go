package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
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
		ID:         ids.New("bkp"),
		ProjectID:  params.ProjectID,
		ScheduleID: params.ScheduleID,
		VolumeID:   source.ID,
		NodeID:     source.NodeID,
		Name:       params.Name,
		State:      StatePending,
		CreatedAt:  now,
		UpdatedAt:  now,
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

	source, err := s.volumes.Source(ctx, volumeID, projectID)
	if err != nil {
		return nil, err
	}

	backups, err := s.repo.listForVolume(ctx, source.ID)
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

	stored, err := s.vault.write(b.ID, content, MaxBytes)
	if err != nil {
		markErr := s.repo.mark(ctx, b.ID, StateFailed, err.Error(), 0, "", "", s.now())
		if markErr != nil {
			return Backup{}, fault.Internal(markErr)
		}
		return Backup{}, fault.Internal(err)
	}

	at := s.now()
	if err := s.repo.mark(ctx, b.ID, StateReady, "",
		stored.Size, stored.Checksum, stored.KeyID, at); err != nil {
		return Backup{}, translate(err)
	}

	b.State = StateReady
	b.SizeBytes = stored.Size
	b.Checksum = stored.Checksum
	b.KeyID = stored.KeyID
	b.UpdatedAt = at

	if b.ScheduleID != "" {
		sc, err := s.repo.scheduleByID(ctx, b.ScheduleID)
		if err != nil && !errors.Is(err, errNotFound) {
			return b, fault.Internal(err)
		}
		if err == nil {
			if _, err := s.retain(ctx, sc); err != nil {
				return b, fault.Internal(err)
			}
		}
	}
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
	return translate(s.repo.mark(ctx, b.ID, StateFailed, message, 0, "", "", s.now()))
}

func (s *service) content(ctx context.Context, id string) (io.ReadCloser, int64, error) {
	b, err := s.repo.byID(ctx, id)
	if err != nil {
		return nil, 0, translate(err)
	}
	if b.State != StateReady {
		return nil, 0, fault.Conflict("backup_not_ready",
			"the backup is "+b.State+" and holds no bytes yet")
	}

	reader, err := s.vault.open(b.ID, b.KeyID)
	if err != nil {
		return nil, 0, fault.Unavailable("backup_unreadable", err.Error())
	}
	return reader, b.SizeBytes, nil
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

func (s *service) setSchedule(ctx context.Context, params ScheduleParams) (Schedule, error) {
	every, err := interval.Parse(params.Every)
	if err != nil {
		return Schedule{}, fault.Invalid("invalid_interval", err.Error())
	}
	if every < MinEvery || every > MaxEvery {
		return Schedule{}, fault.Invalid("invalid_interval",
			"the interval must be between "+MinEvery.String()+" and "+MaxEvery.String())
	}
	if params.Keep < MinKeep || params.Keep > MaxKeep {
		return Schedule{}, fault.Invalid("invalid_keep", fmt.Sprintf(
			"keep must be between %d and %d, because a schedule that keeps nothing "+
				"copies for no reason", MinKeep, MaxKeep))
	}

	if s.volumes == nil {
		return Schedule{}, fault.Unavailable("volumes_unavailable",
			"the platform cannot look up the volume")
	}

	source, err := s.volumes.Source(ctx, params.VolumeID, params.ProjectID)
	if err != nil {
		return Schedule{}, err
	}

	now := s.now()
	sc := Schedule{
		ID:        ids.New("bsc"),
		ProjectID: params.ProjectID,
		VolumeID:  source.ID,
		Every:     every,
		Keep:      params.Keep,
		NextAt:    now.Add(every),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if existing, err := s.repo.scheduleOf(ctx, source.ID); err == nil {
		sc.ID = existing.ID
		sc.CreatedAt = existing.CreatedAt
		sc.LastAt = existing.LastAt
	} else if !errors.Is(err, errNotFound) {
		return Schedule{}, fault.Internal(err)
	}

	if err := s.repo.upsertSchedule(ctx, sc); err != nil {
		return Schedule{}, fault.Internal(err)
	}
	return sc, nil
}

func (s *service) scheduleOf(ctx context.Context, volumeID, projectID string) (Schedule, error) {
	if s.volumes == nil {
		return Schedule{}, fault.Unavailable("volumes_unavailable",
			"the platform cannot look up the volume")
	}

	source, err := s.volumes.Source(ctx, volumeID, projectID)
	if err != nil {
		return Schedule{}, err
	}

	sc, err := s.repo.scheduleOf(ctx, source.ID)
	if errors.Is(err, errNotFound) {
		return Schedule{}, fault.NotFound("schedule_not_found",
			"that volume carries no backup schedule")
	}
	if err != nil {
		return Schedule{}, fault.Internal(err)
	}
	return sc, nil
}

func (s *service) schedulesIn(ctx context.Context, projectID string) ([]Schedule, error) {
	schedules, err := s.repo.schedulesIn(ctx, projectID)
	if err != nil {
		return nil, fault.Internal(err)
	}
	return schedules, nil
}

func (s *service) removeSchedule(ctx context.Context, volumeID, projectID string) error {
	sc, err := s.scheduleOf(ctx, volumeID, projectID)
	if err != nil {
		return err
	}
	if err := s.repo.deleteSchedule(ctx, sc.VolumeID); err != nil {
		return fault.Internal(err)
	}
	return nil
}

func (s *service) sweep(ctx context.Context) (taken, pruned int, err error) {
	now := s.now()

	due, err := s.repo.schedulesDue(ctx, now)
	if err != nil {
		return 0, 0, err
	}

	for _, sc := range due {
		fired, err := s.fire(ctx, sc, now)
		if err != nil {
			return taken, pruned, err
		}
		if fired {
			taken++
		}

		gone, err := s.retain(ctx, sc)
		if err != nil {
			return taken, pruned, err
		}
		pruned += gone
	}
	return taken, pruned, nil
}

func (s *service) fire(ctx context.Context, sc Schedule, now time.Time) (bool, error) {
	if err := s.repo.markScheduleFired(ctx, sc.ID, now, now.Add(sc.Every)); err != nil {
		return false, err
	}

	waiting, err := s.repo.pendingForVolume(ctx, sc.VolumeID)
	if err != nil {
		return false, err
	}
	if waiting > 0 {
		return false, nil
	}

	_, err = s.create(ctx, CreateParams{
		ProjectID:  sc.ProjectID,
		VolumeID:   sc.VolumeID,
		Name:       "auto-" + now.Format("20060102-150405"),
		ScheduleID: sc.ID,
	})
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (s *service) retain(ctx context.Context, sc Schedule) (int, error) {
	made, err := s.repo.madeBySchedule(ctx, sc.ID)
	if err != nil {
		return 0, err
	}

	keepable := make([]Backup, 0, len(made))
	for _, b := range made {
		if b.State == StateReady {
			keepable = append(keepable, b)
		}
	}
	if len(keepable) <= sc.Keep {
		return 0, nil
	}

	pruned := 0
	for _, b := range keepable[sc.Keep:] {
		if err := s.vault.remove(b.ID); err != nil {
			return pruned, err
		}
		if err := s.repo.delete(ctx, b.ID); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}
