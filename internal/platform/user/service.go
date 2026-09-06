package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ratelimit"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s.]+(\.[^@\s.]+)+$`)

type Tokens interface {
	Issue(ctx context.Context, name, role, projectID, userID string,
		lifetime time.Duration) (string, error)
	Forget(ctx context.Context, id string) error
	ForgetUser(ctx context.Context, userID string) error
}

type Roles interface {
	Names() []string
}

type clock func() time.Time

type service struct {
	repo   *repository
	tokens Tokens
	roles  Roles
	events events.Recorder
	logins *ratelimit.Limiter
	log    *slog.Logger
	now    clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{
		repo:   repo,
		log:    log,
		now:    now,
		logins: newLoginLimiter(now),
	}
}

func newLoginLimiter(now clock) *ratelimit.Limiter {
	return ratelimit.New(LoginsPerSecond, LoginBurst, now)
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *service) checkPassword(password string) error {
	if len(password) < MinPasswordLength {
		return fault.Invalid("weak_password", fmt.Sprintf(
			"a password must be at least %d characters", MinPasswordLength))
	}
	if len(password) > MaxPasswordLength {
		return fault.Invalid("invalid_password", fmt.Sprintf(
			"a password must be at most %d characters, because the work of hashing one "+
				"is paid by this server", MaxPasswordLength))
	}
	return nil
}

func (s *service) create(ctx context.Context, params CreateParams) (User, error) {
	email := normalizeEmail(params.Email)
	if !emailPattern.MatchString(email) || len(email) > MaxEmailLength {
		return User{}, fault.Invalid("invalid_email", "that is not an email address")
	}
	if err := validate.Name("name", params.Name); err != nil {
		return User{}, err
	}
	if len(params.Name) > MaxNameLength {
		return User{}, fault.Invalid("invalid_name", fmt.Sprintf(
			"a name must be at most %d characters", MaxNameLength))
	}
	if s.roles != nil {
		if err := validate.OneOf("role", params.Role, s.roles.Names()...); err != nil {
			return User{}, err
		}
	}
	if err := s.checkPassword(params.Password); err != nil {
		return User{}, err
	}

	stored, err := hashPassword(params.Password)
	if err != nil {
		return User{}, fault.Internal(err)
	}

	at := s.now()
	person := User{
		ID:        ids.New("usr"),
		Email:     email,
		Name:      params.Name,
		Role:      params.Role,
		ProjectID: params.ProjectID,
		CreatedAt: at,
		UpdatedAt: at,
	}

	if err := s.repo.insert(ctx, person, stored); err != nil {
		return User{}, translate(err)
	}
	return person, nil
}

func (s *service) find(ctx context.Context, id string) (User, error) {
	person, err := s.repo.byID(ctx, id)
	if errors.Is(err, errNotFound) {
		person, err = s.repo.byEmail(ctx, normalizeEmail(id))
	}
	if err != nil {
		return User{}, translate(err)
	}
	return person, nil
}

func (s *service) list(ctx context.Context) ([]User, error) {
	people, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return people, nil
}

func (s *service) setPassword(ctx context.Context, id, password string) error {
	person, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if err := s.checkPassword(password); err != nil {
		return err
	}

	stored, err := hashPassword(password)
	if err != nil {
		return fault.Internal(err)
	}
	if err := s.repo.setPassword(ctx, person.ID, stored, s.now()); err != nil {
		return translate(err)
	}

	if s.tokens != nil {
		if err := s.tokens.ForgetUser(ctx, person.ID); err != nil {
			return fault.Internal(fmt.Errorf(
				"the password was changed but the sessions it replaces are still valid: %w",
				err))
		}
	}

	s.note(ctx, person, "user.password_changed",
		person.Email+" has a new password, and every session it replaces is gone",
		events.Warn)
	return nil
}

func (s *service) setDisabled(ctx context.Context, id string, disabled bool) (User, error) {
	person, err := s.find(ctx, id)
	if err != nil {
		return User{}, err
	}
	if err := s.repo.setDisabled(ctx, person.ID, disabled, s.now()); err != nil {
		return User{}, translate(err)
	}

	kind, message := "user.enabled", person.Email+" can sign in again"
	if disabled {
		kind, message = "user.disabled", person.Email+" can no longer sign in, "+
			"and every token they hold stops working with them"
	}
	s.note(ctx, person, kind, message, events.Warn)

	return s.find(ctx, person.ID)
}

func (s *service) remove(ctx context.Context, id string) error {
	person, err := s.find(ctx, id)
	if err != nil {
		return err
	}

	if s.tokens != nil {
		if err := s.tokens.ForgetUser(ctx, person.ID); err != nil {
			return fault.Internal(fmt.Errorf(
				"person %s was not deleted, because their tokens could not be: %w",
				person.Email, err))
		}
	}
	if err := s.repo.delete(ctx, person.ID); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) Allowed(ctx context.Context, userID string) (bool, error) {
	person, err := s.repo.byID(ctx, userID)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !person.Disabled, nil
}

func (s *service) login(ctx context.Context, remote, email, password string) (Session, error) {
	if allowed, wait := s.logins.Allow(loginKey(remote)); !allowed {
		return Session{}, fault.TooMany("too_many_attempts",
			"too many sign in attempts, try again in "+wait.Round(time.Second).String())
	}
	if s.tokens == nil {
		return Session{}, fault.Internal(errors.New("this control plane cannot issue tokens"))
	}

	person, stored, err := s.repo.credentials(ctx, normalizeEmail(email))
	if errors.Is(err, errNotFound) {
		spendTheSameTime(password)
		return Session{}, refuse()
	}
	if err != nil {
		return Session{}, err
	}

	same, err := matches(stored, password)
	if err != nil {
		return Session{}, fault.Internal(err)
	}
	if !same || person.Disabled {
		return Session{}, refuse()
	}

	secret, err := s.tokens.Issue(ctx, sessionName(person.ID), person.Role,
		person.ProjectID, person.ID, SessionLifetime)
	if err != nil {
		return Session{}, err
	}

	s.note(ctx, person, "user.signed_in", person.Email+" signed in", events.Info)

	return Session{
		Secret:    secret,
		UserID:    person.ID,
		Email:     person.Email,
		Role:      person.Role,
		ProjectID: person.ProjectID,
		ExpiresAt: s.now().Add(SessionLifetime),
	}, nil
}

func (s *service) logout(ctx context.Context) error {
	tokenID := scope.From(ctx).TokenID
	if tokenID == "" || s.tokens == nil {
		return nil
	}

	err := s.tokens.Forget(ctx, tokenID)
	var refused *fault.Fault
	if errors.As(err, &refused) && refused.Code == "last_admin_token" {
		s.log.Warn("a session was signed out but its token was kept, because revoking the "+
			"only admin token that still works would lock everyone out",
			"token", tokenID)
		return nil
	}
	return err
}

func sessionName(userID string) string {
	suffix := ids.New("s")
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return "session-" + strings.TrimPrefix(userID, "usr-") + "-" + suffix
}

func refuse() error {
	return fault.Unauthenticated("bad_credentials",
		"that email and password do not match an account that can sign in")
}

func loginKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	return "l:" + host
}

func (s *service) note(ctx context.Context, person User, kind, message string,
	severity events.Severity) {
	if s.events == nil {
		return
	}
	s.events.Record(ctx, events.Entry{
		ProjectID: person.ProjectID,
		Kind:      kind,
		Subject:   person.ID,
		Message:   message,
		Severity:  severity,
	})
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("user_not_found", "no person with that id or email exists")
	case errors.Is(err, errEmailUsed):
		return fault.Conflict("email_taken", "somebody already signs in with that address")
	default:
		return err
	}
}
