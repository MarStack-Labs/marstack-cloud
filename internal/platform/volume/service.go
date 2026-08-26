package volume

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Instances interface {
	Placement(ctx context.Context, instanceID string) (Placement, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	instances Instances
	now       clock
}

func newService(repo *repository, instances Instances, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, instances: instances, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Volume, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Volume{}, err
	}
	if params.SizeGiB < MinSizeGiB || params.SizeGiB > MaxSizeGiB {
		return Volume{}, fault.Invalid("invalid_size", fmt.Sprintf(
			"size_gib must be between %d and %d", MinSizeGiB, MaxSizeGiB,
		))
	}

	now := s.now()
	v := Volume{
		ID:        ids.New("vol"),
		Name:      params.Name,
		SizeGiB:   params.SizeGiB,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.insert(ctx, v); err != nil {
		return Volume{}, translate(err)
	}
	return v, nil
}

func (s *service) resolve(ctx context.Context, nameOrID string) (Volume, error) {
	v, err := s.repo.byName(ctx, nameOrID)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, errNotFound) {
		return Volume{}, translate(err)
	}

	v, err = s.repo.byID(ctx, nameOrID)
	if err != nil {
		return Volume{}, translate(err)
	}
	return v, nil
}

func (s *service) list(ctx context.Context) ([]Volume, error) {
	volumes, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return volumes, nil
}

func (s *service) onNode(ctx context.Context, nodeID string) ([]Volume, error) {
	volumes, err := s.repo.onNode(ctx, nodeID)
	if err != nil {
		return nil, translate(err)
	}
	return volumes, nil
}

func (s *service) attach(ctx context.Context, nameOrID, instanceID string) (Volume, error) {
	v, err := s.resolve(ctx, nameOrID)
	if err != nil {
		return Volume{}, err
	}
	if v.InstanceID == instanceID {
		return v, nil
	}
	if v.InstanceID != "" {
		return Volume{}, fault.Conflict("volume_attached",
			"the volume is attached to "+v.InstanceID+", and a disk cannot have two writers")
	}
	if s.instances == nil {
		return Volume{}, fault.Unavailable("instances_unavailable",
			"the platform cannot look up where an instance runs")
	}

	placed, err := s.instances.Placement(ctx, instanceID)
	if err != nil {
		return Volume{}, err
	}
	if placed.NodeID == "" {
		return Volume{}, fault.Conflict("instance_unplaced",
			"the instance has no node yet, and a volume lives on the node it is attached to")
	}
	if placed.Isolation != "vm" {
		return Volume{}, fault.Invalid("unsupported_isolation",
			"only isolation vm takes a volume today, because the others have no second disk")
	}
	if v.NodeID != "" && v.NodeID != placed.NodeID {
		return Volume{}, fault.Conflict("volume_elsewhere",
			"the volume holds data on node "+v.NodeID+" and cannot follow an instance to "+
				placed.NodeID)
	}

	if err := s.repo.attach(ctx, v.ID, instanceID, placed.NodeID, s.now()); err != nil {
		return Volume{}, translate(err)
	}

	v.InstanceID = instanceID
	v.NodeID = placed.NodeID
	return v, nil
}

func (s *service) detach(ctx context.Context, nameOrID string) (Volume, error) {
	v, err := s.resolve(ctx, nameOrID)
	if err != nil {
		return Volume{}, err
	}
	if v.InstanceID == "" {
		return v, nil
	}

	if err := s.repo.detach(ctx, v.ID, s.now()); err != nil {
		return Volume{}, translate(err)
	}

	v.InstanceID = ""
	return v, nil
}

func (s *service) releaseInstance(ctx context.Context, instanceID string) error {
	if err := s.repo.detachInstance(ctx, instanceID, s.now()); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) remove(ctx context.Context, nameOrID string) error {
	v, err := s.resolve(ctx, nameOrID)
	if err != nil {
		return err
	}
	if v.InstanceID != "" {
		return fault.Conflict("volume_attached",
			"detach the volume from "+v.InstanceID+" before deleting it")
	}

	if err := s.repo.delete(ctx, v.ID); err != nil {
		return translate(err)
	}
	return nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("volume_not_found", "no volume with that name or id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("volume_name_taken", "a volume with that name already exists")
	case errors.Is(err, errTaken):
		return fault.Conflict("volume_attached", "the volume was attached by someone else")
	default:
		return err
	}
}
