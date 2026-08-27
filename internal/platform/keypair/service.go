package keypair

import (
	"context"
	"errors"
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

func (s *service) create(ctx context.Context, params CreateParams) (Key, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Key{}, err
	}

	key, err := parsePublicKey(params.PublicKey)
	if err != nil {
		return Key{}, fault.Invalid("invalid_public_key", err.Error())
	}

	k := Key{
		ID:          ids.New("key"),
		ProjectID:   params.ProjectID,
		Name:        params.Name,
		PublicKey:   key.line(),
		Fingerprint: key.Fingerprint,
		Kind:        key.Kind,
		Comment:     key.Comment,
		CreatedAt:   s.now(),
	}

	if err := s.repo.insert(ctx, k); err != nil {
		return Key{}, translate(err)
	}
	return k, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Key, error) {
	keys, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return keys, nil
}

func (s *service) resolve(ctx context.Context, projectID string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > MaxPerName {
		return nil, fault.Invalid("too_many_keys", "an instance takes at most 64 keys")
	}

	seen := map[string]bool{}
	lines := make([]string, 0, len(names))

	for _, name := range names {
		k, err := s.repo.byName(ctx, projectID, name)
		if errors.Is(err, errNotFound) {
			k, err = s.repo.byID(ctx, projectID, name)
		}
		if errors.Is(err, errNotFound) {
			return nil, fault.Invalid("unknown_key",
				"no key named "+name+" exists in this project")
		}
		if err != nil {
			return nil, translate(err)
		}

		if seen[k.Fingerprint] {
			continue
		}
		seen[k.Fingerprint] = true
		lines = append(lines, k.PublicKey)
	}
	return lines, nil
}

func (s *service) remove(ctx context.Context, projectID, nameOrID string) error {
	k, err := s.repo.byName(ctx, projectID, nameOrID)
	if errors.Is(err, errNotFound) {
		k, err = s.repo.byID(ctx, projectID, nameOrID)
	}
	if err != nil {
		return translate(err)
	}
	return translate(s.repo.delete(ctx, projectID, k.ID))
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("key_not_found", "no key with that name or id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("key_name_taken", "a key with that name already exists")
	default:
		return fault.Internal(err)
	}
}
