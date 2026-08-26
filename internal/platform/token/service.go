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

func (s *service) create(ctx context.Context, params CreateParams) (Token, string, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Token{}, "", err
	}
	if err := validate.OneOf("role", params.Role, Roles()...); err != nil {
		return Token{}, "", err
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
		CreatedAt:  now,
		LastUsedAt: now,
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

	if err := s.repo.touch(ctx, identity.ID, s.now()); err != nil {
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
	admins, err := s.repo.countByRole(ctx, RoleAdmin)
	if err != nil {
		return translate(err)
	}

	tokens, err := s.repo.list(ctx)
	if err != nil {
		return translate(err)
	}

	for _, t := range tokens {
		if t.ID != id {
			continue
		}
		if t.Role == RoleAdmin && admins == 1 {
			return fault.Conflict("last_admin_token",
				"this is the only admin token, and revoking it locks everyone out")
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
