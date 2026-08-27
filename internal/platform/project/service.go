package project

import (
	"context"
	"errors"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Occupancy interface {
	ResourcesIn(ctx context.Context, projectID string) (int, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	occupancy Occupancy
	now       clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Project, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Project{}, err
	}

	p := Project{
		ID:        ids.New("prj"),
		Name:      params.Name,
		CreatedAt: s.now(),
	}
	if err := s.repo.insert(ctx, p); err != nil {
		return Project{}, translate(err)
	}
	return p, nil
}

func (s *service) list(ctx context.Context) ([]Project, error) {
	projects, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return projects, nil
}

func (s *service) get(ctx context.Context, id string) (Project, error) {
	p, err := s.repo.byID(ctx, id)
	if err != nil {
		return Project{}, translate(err)
	}
	return p, nil
}

func (s *service) exists(ctx context.Context, id string) (bool, error) {
	if _, err := s.repo.byID(ctx, id); err != nil {
		if errors.Is(err, errNotFound) {
			return false, nil
		}
		return false, translate(err)
	}
	return true, nil
}

func (s *service) remove(ctx context.Context, id string) error {
	if id == DefaultID {
		return fault.Conflict("default_project",
			"the default project holds every token that names no project, so it cannot be deleted")
	}
	if _, err := s.repo.byID(ctx, id); err != nil {
		return translate(err)
	}

	if s.occupancy == nil {
		return fault.Internal(errors.New("no occupancy source is wired, so emptiness cannot be proven"))
	}

	held, err := s.occupancy.ResourcesIn(ctx, id)
	if err != nil {
		return fault.Internal(err)
	}
	if held > 0 {
		return fault.Conflict("project_in_use",
			"the project still holds resources, and deleting it would leave them unreachable")
	}

	if err := s.repo.delete(ctx, id); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) ensureDefault(ctx context.Context) (Project, error) {
	existing, err := s.repo.byID(ctx, DefaultID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, errNotFound) {
		return Project{}, translate(err)
	}

	p := Project{ID: DefaultID, Name: DefaultName, CreatedAt: s.now()}
	if err := s.repo.insert(ctx, p); err != nil {
		if errors.Is(err, errNameTaken) {
			return s.repo.byName(ctx, DefaultName)
		}
		return Project{}, translate(err)
	}
	return p, nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("project_not_found", "no project with that id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("project_name_taken", "a project with that name already exists")
	default:
		return fault.Internal(err)
	}
}
