package acme

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/acme"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/certs"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
)

type Balancers interface {
	HostsOf(ctx context.Context, id, projectID string) ([]string, error)
	AttachCertificate(ctx context.Context, id, projectID, certPEM, keyPEM string) error
}

type clock func() time.Time

type service struct {
	repo      *repository
	balancers Balancers
	events    events.Recorder
	log       *slog.Logger
	now       clock

	directory string
	contact   string
	client    *acme.Client
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
}

func (s *service) enabled() bool {
	return s.directory != "" && s.balancers != nil
}

func checkNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, fault.Invalid("no_names",
			"name at least one host to get a certificate for, or give the balancer routes "+
				"and they are used")
	}
	if len(names) > MaxNames {
		return nil, fault.Invalid("too_many_names", fmt.Sprintf(
			"one certificate covers at most %d names", MaxNames))
	}

	seen := map[string]bool{}
	held := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || len(name) > MaxNameChars || strings.ContainsAny(name, "/: ") {
			return nil, fault.Invalid("invalid_name", name+" is not a host name")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		held = append(held, name)
	}
	return held, nil
}

func (s *service) start(ctx context.Context, params StartParams) (Order, error) {
	if !s.enabled() {
		return Order{}, fault.Conflict("no_authority",
			"this control plane has no certificate authority to ask: start it with "+
				"--acme-directory")
	}

	names := params.Names
	if len(names) == 0 {
		hosts, err := s.balancers.HostsOf(ctx, params.BalancerID, params.ProjectID)
		if err != nil {
			return Order{}, err
		}
		names = hosts
	}

	names, err := checkNames(names)
	if err != nil {
		return Order{}, err
	}

	at := s.now()
	held := Order{
		ID:         ids.New("acme"),
		ProjectID:  params.ProjectID,
		BalancerID: params.BalancerID,
		Names:      names,
		State:      StatePending,
		Message:    "waiting for the first pass",
		CreatedAt:  at,
		UpdatedAt:  at,
	}

	existing, err := s.repo.forBalancer(ctx, params.BalancerID)
	if err == nil {
		held.ID = existing.ID
		held.CreatedAt = existing.CreatedAt
	} else if !errors.Is(err, errNotFound) {
		return Order{}, err
	}

	if err := s.repo.save(ctx, held); err != nil {
		return Order{}, err
	}
	return held, nil
}

func (s *service) forBalancer(ctx context.Context, balancerID, projectID string) (Order, error) {
	held, err := s.repo.forBalancer(ctx, balancerID)
	if errors.Is(err, errNotFound) || (err == nil && held.ProjectID != projectID) {
		return Order{}, fault.NotFound("order_not_found",
			"no certificate is being kept for that balancer")
	}
	return held, err
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Order, error) {
	return s.repo.listIn(ctx, projectID)
}

func (s *service) remove(ctx context.Context, balancerID, projectID string) error {
	if _, err := s.forBalancer(ctx, balancerID, projectID); err != nil {
		return err
	}
	return s.repo.delete(ctx, balancerID)
}

func (s *service) tokens(ctx context.Context) (map[string]string, error) {
	held, err := s.repo.all(ctx)
	if err != nil {
		return nil, err
	}

	tokens := map[string]string{}
	for _, one := range held {
		for _, challenge := range one.Challenges {
			if challenge.Token != "" && challenge.Authorization != "" {
				tokens[challenge.Token] = challenge.Authorization
			}
		}
	}
	return tokens, nil
}

func (s *service) note(ctx context.Context, held Order, kind, message string,
	severity events.Severity) {
	if s.events == nil {
		return
	}
	s.events.Record(ctx, events.Entry{
		ProjectID: held.ProjectID,
		Subject:   held.BalancerID,
		Kind:      kind,
		Severity:  severity,
		Message:   message,
	})
}

func (s *service) fail(ctx context.Context, held Order, reason string) {
	held.State = StateFailed
	held.Message = reason
	held.Challenges = nil
	held.UpdatedAt = s.now()

	if err := s.repo.save(ctx, held); err != nil {
		s.log.Warn("could not record a failed order", "balancer", held.BalancerID,
			"error", err)
	}
	s.note(ctx, held, "certificate.order_failed",
		strings.Join(held.Names, ", ")+": "+reason, events.Error)
}

func (s *service) attach(ctx context.Context, held Order, chain, key string) error {
	summary, err := certs.Inspect(chain, key)
	if err != nil {
		return fmt.Errorf("the issued certificate is not usable: %w", err)
	}

	if err := s.balancers.AttachCertificate(
		ctx, held.BalancerID, held.ProjectID, chain, key); err != nil {
		return err
	}

	held.State = StateIssued
	held.Message = "issued for " + strings.Join(held.Names, ", ")
	held.ExpiresAt = summary.ExpiresAt
	held.IssuedAt = s.now()
	held.UpdatedAt = s.now()
	held.Challenges = nil
	held.KeyPEM = ""
	held.OrderURL = ""

	if err := s.repo.save(ctx, held); err != nil {
		return err
	}

	s.note(ctx, held, "certificate.issued",
		strings.Join(held.Names, ", ")+" is covered until "+summary.ExpiresAt, events.Info)
	return nil
}
