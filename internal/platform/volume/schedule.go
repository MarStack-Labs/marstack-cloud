package volume

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
)

const (
	MinEvery = time.Minute
	MaxEvery = 365 * 24 * time.Hour
	MinKeep  = 1
	MaxKeep  = 365

	SweepInterval = time.Minute
)

type Schedule struct {
	ID        string
	ProjectID string
	VolumeID  string
	Every     time.Duration
	Keep      int
	NextAt    time.Time
	LastAt    time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ScheduleParams struct {
	ProjectID string
	VolumeID  string
	Every     string
	Keep      int
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
				"snapshots for no reason", MinKeep, MaxKeep))
	}

	v, err := s.resolveIn(ctx, params.VolumeID, params.ProjectID)
	if err != nil {
		return Schedule{}, err
	}

	now := s.now()
	sc := Schedule{
		ID:        ids.New("ssc"),
		ProjectID: params.ProjectID,
		VolumeID:  v.ID,
		Every:     every,
		Keep:      params.Keep,
		NextAt:    now.Add(every),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if existing, err := s.repo.scheduleOf(ctx, v.ID); err == nil {
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

func (s *service) clearSchedule(ctx context.Context, volumeID, projectID string) error {
	v, err := s.resolveIn(ctx, volumeID, projectID)
	if err != nil {
		return err
	}
	return translate(s.repo.deleteSchedule(ctx, v.ID))
}

func (s *service) schedulesIn(ctx context.Context, projectID string) ([]Schedule, error) {
	schedules, err := s.repo.schedulesIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return schedules, nil
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

	waiting, err := s.repo.pendingSnapshotsForVolume(ctx, sc.VolumeID)
	if err != nil {
		return false, err
	}
	if waiting > 0 {
		return false, nil
	}

	_, err = s.snapshotWithSchedule(ctx, sc.VolumeID, sc.ProjectID,
		"auto-"+now.Format("20060102-150405"), sc.ID)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (s *service) retain(ctx context.Context, sc Schedule) (int, error) {
	made, err := s.repo.snapshotsBySchedule(ctx, sc.ID)
	if err != nil {
		return 0, err
	}

	keepable := make([]Snapshot, 0, len(made))
	for _, snap := range made {
		if snap.State == SnapshotReady {
			keepable = append(keepable, snap)
		}
	}
	if len(keepable) <= sc.Keep {
		return 0, nil
	}

	pruned := 0
	for _, snap := range keepable[sc.Keep:] {
		if err := s.repo.deleteSnapshot(ctx, snap.ID); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}
