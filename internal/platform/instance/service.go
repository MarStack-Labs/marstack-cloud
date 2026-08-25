package instance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type clock func() time.Time

type service struct {
	repo *repository
	now  clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Instance, error) {
	normalized, err := normalize(params)
	if err != nil {
		return Instance{}, err
	}

	now := s.now()
	in := Instance{
		ID:        ids.New("i"),
		Name:      normalized.Name,
		Isolation: Isolation(normalized.Isolation),
		Image:     normalized.Image,
		VCPU:      normalized.VCPU,
		MemoryMiB: normalized.MemoryMiB,
		Desired:   DesiredRunning,
		Observed:  ObservedPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.insert(ctx, in); err != nil {
		return Instance{}, translate(err)
	}
	return in, nil
}

func (s *service) get(ctx context.Context, id string) (Instance, error) {
	in, err := s.repo.get(ctx, id)
	if err != nil {
		return Instance{}, translate(err)
	}
	return in, nil
}

func (s *service) list(ctx context.Context) ([]Instance, error) {
	instances, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return instances, nil
}

func (s *service) setDesired(ctx context.Context, id string, desired DesiredState) (Instance, error) {
	if err := s.repo.setDesired(ctx, id, desired, s.now()); err != nil {
		return Instance{}, translate(err)
	}
	return s.get(ctx, id)
}

func (s *service) delete(ctx context.Context, id string) error {
	return translate(s.repo.delete(ctx, id))
}

func normalize(params CreateParams) (CreateParams, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return params, err
	}
	if err := validate.OneOf("isolation", params.Isolation, AllIsolations()...); err != nil {
		return params, err
	}
	if params.Image == "" {
		return params, fault.Invalid("invalid_image", "image must not be empty")
	}

	if params.VCPU == 0 {
		params.VCPU = DefaultVCPU
	}
	if params.MemoryMiB == 0 {
		params.MemoryMiB = DefaultMemoryMiB
	}

	if params.VCPU < MinVCPU || params.VCPU > MaxVCPU {
		return params, fault.Invalid("invalid_vcpu", fmt.Sprintf(
			"vcpu must be between %d and %d", MinVCPU, MaxVCPU,
		))
	}
	if params.MemoryMiB < MinMemoryMiB || params.MemoryMiB > MaxMemoryMiB {
		return params, fault.Invalid("invalid_memory", fmt.Sprintf(
			"memory_mib must be between %d and %d", MinMemoryMiB, MaxMemoryMiB,
		))
	}

	return params, nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("instance_not_found", "no instance with that id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("instance_name_taken", "an instance with that name already exists")
	default:
		return fault.Internal(err)
	}
}
