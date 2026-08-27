package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

const (
	secretBytes  = 24
	secretPrefix = "mst_"
)

var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

type clock func() time.Time

type Projects interface {
	Exists(ctx context.Context, id string) (bool, error)
}

type service struct {
	repo     *repository
	projects Projects
	now      clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Token, string, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Token{}, "", err
	}
	if err := validate.OneOf("role", params.Role, Roles()...); err != nil {
		return Token{}, "", err
	}
	if params.ProjectID == "" {
		params.ProjectID = defaultProjectID
	}
	if err := s.requireProject(ctx, params.ProjectID); err != nil {
		return Token{}, "", err
	}

	lifetime, err := ParseLifetime(params.ExpiresIn)
	if err != nil {
		return Token{}, "", fault.Invalid("invalid_lifetime", err.Error())
	}
	if lifetime < 0 {
		return Token{}, "", fault.Invalid("invalid_lifetime",
			"a lifetime in the past would create a token nobody can use")
	}
	if lifetime > MaxLifetime {
		return Token{}, "", fault.Invalid("invalid_lifetime",
			"a lifetime beyond ten years is the same as no lifetime, so say so instead")
	}

	secret, err := newSecret()
	if err != nil {
		return Token{}, "", fault.Internal(err)
	}

	now := s.now()
	t := Token{
		ID:         ids.New("tok"),
		Name:       params.Name,
		Role:       params.Role,
		ProjectID:  params.ProjectID,
		CreatedAt:  now,
		LastUsedAt: now,
	}
	if lifetime > 0 {
		t.ExpiresAt = now.Add(lifetime)
	}

	if err := s.repo.insert(ctx, t, hashOf(secret)); err != nil {
		return Token{}, "", translate(err)
	}
	return t, secret, nil
}

func (s *service) verify(ctx context.Context, secret string) (Identity, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Identity{}, fault.Unauthenticated("missing_token", "no bearer token was given")
	}

	identity, err := s.repo.byHash(ctx, hashOf(secret))
	if errors.Is(err, errNotFound) {
		return Identity{}, fault.Unauthenticated("unknown_token", "the bearer token is not valid")
	}
	if err != nil {
		return Identity{}, err
	}

	now := s.now()
	if !identity.ExpiresAt.IsZero() && !now.Before(identity.ExpiresAt) {
		return Identity{}, fault.Unauthenticated("token_expired",
			"the bearer token expired on "+identity.ExpiresAt.Format(time.RFC3339))
	}

	if err := s.repo.touch(ctx, identity.ID, now); err != nil {
		return identity, nil
	}
	return identity, nil
}

func (s *service) list(ctx context.Context) ([]Token, error) {
	tokens, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return tokens, nil
}

func (s *service) remove(ctx context.Context, id string) error {
	tokens, err := s.repo.list(ctx)
	if err != nil {
		return translate(err)
	}

	now := s.now()
	usableAdmins := 0
	for _, t := range tokens {
		if t.Role == RoleAdmin && !t.Expired(now) {
			usableAdmins++
		}
	}

	for _, t := range tokens {
		if t.ID != id {
			continue
		}
		if t.Role == RoleAdmin && !t.Expired(now) && usableAdmins == 1 {
			return fault.Conflict("last_admin_token",
				"this is the only admin token that still works, and revoking it locks everyone out")
		}
	}

	if err := s.repo.delete(ctx, id); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) ensureBootstrap(ctx context.Context) (string, error) {
	count, err := s.repo.count(ctx)
	if err != nil {
		return "", translate(err)
	}
	if count > 0 {
		return "", nil
	}

	_, secret, err := s.create(ctx, CreateParams{Name: BootstrapName, Role: RoleAdmin})
	if err != nil {
		return "", err
	}
	return secret, nil
}

func (s *service) requireProject(ctx context.Context, id string) error {
	if s.projects == nil {
		return fault.Internal(errors.New("no project source is wired, so a token cannot be placed"))
	}

	exists, err := s.projects.Exists(ctx, id)
	if err != nil {
		return fault.Internal(err)
	}
	if !exists {
		return fault.Invalid("unknown_project", "no project with that id exists")
	}
	return nil
}

func newSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return secretPrefix + encoding.EncodeToString(raw), nil
}

func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("token_not_found", "no token with that id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("token_name_taken", "a token with that name already exists")
	default:
		return err
	}
}

func (s *service) countIn(ctx context.Context, projectID string) (int, error) {
	count, err := s.repo.countInProject(ctx, projectID)
	if err != nil {
		return 0, translate(err)
	}
	return count, nil
}
