package node

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

const (
	minCPUs      = 1
	minMemoryMiB = 128
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

func (s *service) register(ctx context.Context, params RegisterParams) (Node, error) {
	if err := validateRegister(params); err != nil {
		return Node{}, err
	}

	now := s.now()

	existing, err := s.repo.findByName(ctx, params.Name)
	switch {
	case err == nil:
		existing.Zone = params.Zone
		existing.Arch = params.Arch
		existing.OS = params.OS
		existing.CPUs = params.CPUs
		existing.MemoryMiB = params.MemoryMiB
		existing.AgentVersion = params.AgentVersion
		existing.LastSeenAt = now

		if err := s.repo.updateOnRegister(ctx, existing); err != nil {
			return Node{}, translate(err)
		}
		return existing, nil

	case errors.Is(err, errNotFound):
		created := Node{
			ID:           ids.New("n"),
			Name:         params.Name,
			Zone:         params.Zone,
			Arch:         params.Arch,
			OS:           params.OS,
			CPUs:         params.CPUs,
			MemoryMiB:    params.MemoryMiB,
			AgentVersion: params.AgentVersion,
			RegisteredAt: now,
			LastSeenAt:   now,
		}
		if err := s.repo.insert(ctx, created); err != nil {
			return Node{}, translate(err)
		}
		return created, nil

	default:
		return Node{}, translate(err)
	}
}

func (s *service) heartbeat(ctx context.Context, id string) (Node, error) {
	if err := s.repo.touch(ctx, id, s.now()); err != nil {
		return Node{}, translate(err)
	}
	return s.get(ctx, id)
}

func (s *service) get(ctx context.Context, id string) (Node, error) {
	n, err := s.repo.get(ctx, id)
	if err != nil {
		return Node{}, translate(err)
	}
	return n, nil
}

func (s *service) list(ctx context.Context) ([]Node, error) {
	nodes, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return nodes, nil
}

func validateRegister(params RegisterParams) error {
	if err := validate.Name("name", params.Name); err != nil {
		return err
	}
	if params.Zone != "" {
		if err := validate.Name("zone", params.Zone); err != nil {
			return err
		}
	}
	if params.Arch == "" {
		return fault.Invalid("invalid_arch", "arch must not be empty")
	}
	if params.OS == "" {
		return fault.Invalid("invalid_os", "os must not be empty")
	}
	if params.CPUs < minCPUs {
		return fault.Invalid("invalid_cpus", fmt.Sprintf("cpus must be at least %d", minCPUs))
	}
	if params.MemoryMiB < minMemoryMiB {
		return fault.Invalid("invalid_memory", fmt.Sprintf("memory_mib must be at least %d", minMemoryMiB))
	}
	return nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("node_not_found", "no node with that id exists")
	default:
		return fault.Internal(err)
	}
}
