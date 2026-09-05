package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

var hostPattern = regexp.MustCompile(
	`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

type clock func() time.Time

type service struct {
	repo *repository
	keys *sealed.Keyring
	now  clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func checkHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))

	if host == "" {
		return "", fault.Invalid("invalid_host", "name the registry host, such as ghcr.io")
	}
	if strings.Contains(host, "/") || strings.Contains(host, "://") {
		return "", fault.Invalid("invalid_host",
			"give the host on its own, without a scheme or a path: ghcr.io, not "+host)
	}
	if len(host) > MaxHostLength || !hostPattern.MatchString(host) {
		return "", fault.Invalid("invalid_host", host+" is not a host name")
	}
	return host, nil
}

func (s *service) create(ctx context.Context, params CreateParams) (Credential, error) {
	host, err := checkHost(params.Host)
	if err != nil {
		return Credential{}, err
	}

	username := strings.TrimSpace(params.Username)
	if username == "" {
		return Credential{}, fault.Invalid("invalid_username",
			"name the user this credential logs in as")
	}
	if len(username) > MaxUsernameLength {
		return Credential{}, fault.Invalid("invalid_username", fmt.Sprintf(
			"a username is at most %d characters", MaxUsernameLength))
	}
	if params.Password == "" {
		return Credential{}, fault.Invalid("invalid_password", "give the password or token")
	}
	if len(params.Password) > MaxPasswordLength {
		return Credential{}, fault.Invalid("invalid_password", fmt.Sprintf(
			"a password is at most %d characters", MaxPasswordLength))
	}

	if s.keys == nil || !s.keys.Sealing() {
		return Credential{}, fault.Conflict("no_sealing_key",
			"this control plane has no key to seal a registry password with, and handing one "+
				"to every node out of a database it cannot protect is not something it will "+
				"do quietly: start it with --backup-key-file")
	}

	active, _ := s.keys.Active()
	blob, err := sealed.SealBytes([]byte(params.Password), active)
	if err != nil {
		return Credential{}, fault.Internal(fmt.Errorf("seal the password: %w", err))
	}

	held := Credential{
		ID:        ids.New("reg"),
		Host:      host,
		Username:  username,
		Sealed:    blob,
		KeyID:     s.keys.ActiveID(),
		CreatedAt: s.now(),
	}

	if err := s.repo.insert(ctx, held); err != nil {
		if errors.Is(err, errHostTaken) {
			return Credential{}, fault.Conflict("host_taken",
				"there is already a credential for "+host+", and one pull cannot be made "+
					"with two logins: delete that one first")
		}
		return Credential{}, err
	}
	return held, nil
}

func (s *service) list(ctx context.Context) ([]Credential, error) {
	return s.repo.all(ctx)
}

func (s *service) remove(ctx context.Context, id string) error {
	err := s.repo.delete(ctx, id)
	if errors.Is(err, errNotFound) {
		return fault.NotFound("credential_not_found",
			"no registry credential with that id or host exists")
	}
	return err
}

func (s *service) forNode(ctx context.Context) ([]Open, error) {
	held, err := s.repo.all(ctx)
	if err != nil {
		return nil, err
	}

	open := make([]Open, 0, len(held))
	for _, one := range held {
		key, found := s.keys.Find(one.KeyID)
		if !found {
			continue
		}

		plain, err := sealed.OpenBytes(one.Sealed, key)
		if err != nil {
			return nil, fault.Internal(fmt.Errorf("unseal a registry password: %w", err))
		}
		open = append(open, Open{
			Host:     one.Host,
			Username: one.Username,
			Password: string(plain),
		})
	}
	return open, nil
}
