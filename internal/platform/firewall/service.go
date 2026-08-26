package firewall

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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

func (s *service) create(ctx context.Context, params CreateParams) (Firewall, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Firewall{}, err
	}

	rules, err := checkRules(params.Rules)
	if err != nil {
		return Firewall{}, err
	}

	now := s.now()
	f := Firewall{
		ID:        ids.New("fw"),
		Name:      params.Name,
		Rules:     rules,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.insert(ctx, f); err != nil {
		return Firewall{}, translate(err)
	}
	return f, nil
}

func checkRules(rules []Rule) ([]Rule, error) {
	if len(rules) > MaxRules {
		return nil, fault.Invalid("invalid_rules", fmt.Sprintf(
			"a firewall may hold at most %d rules", MaxRules))
	}

	checked := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		if rule.Protocol == "" {
			rule.Protocol = ProtocolTCP
		}
		if err := validate.OneOf("protocol", rule.Protocol, Protocols()...); err != nil {
			return nil, err
		}

		if rule.Protocol == ProtocolICMP || rule.Protocol == ProtocolAny {
			rule.FromPort, rule.ToPort = 0, 0
		} else {
			if rule.FromPort == 0 {
				return nil, fault.Invalid("invalid_port",
					"a tcp or udp rule needs from_port")
			}
			if rule.ToPort == 0 {
				rule.ToPort = rule.FromPort
			}
			if rule.FromPort < MinPort || rule.ToPort > MaxPort || rule.ToPort < rule.FromPort {
				return nil, fault.Invalid("invalid_port", fmt.Sprintf(
					"ports must be between %d and %d, and from_port must not exceed to_port",
					MinPort, MaxPort))
			}
		}

		if rule.Source == "" {
			rule.Source = "0.0.0.0/0"
		}
		prefix, err := netip.ParsePrefix(rule.Source)
		if err != nil {
			return nil, fault.Invalid("invalid_source",
				"source must be a CIDR such as 10.20.0.0/16 or 0.0.0.0/0")
		}
		if !prefix.Addr().Is4() {
			return nil, fault.Invalid("invalid_source", "only IPv4 sources are supported")
		}
		rule.Source = prefix.Masked().String()

		checked = append(checked, rule)
	}
	return checked, nil
}

func (s *service) resolve(ctx context.Context, nameOrID string) (Firewall, error) {
	f, err := s.repo.byName(ctx, nameOrID)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, errNotFound) {
		return Firewall{}, translate(err)
	}

	f, err = s.repo.byID(ctx, nameOrID)
	if err != nil {
		return Firewall{}, translate(err)
	}
	return f, nil
}

func (s *service) list(ctx context.Context) ([]Firewall, error) {
	firewalls, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return firewalls, nil
}

func (s *service) setRules(ctx context.Context, nameOrID string, rules []Rule) (Firewall, error) {
	f, err := s.resolve(ctx, nameOrID)
	if err != nil {
		return Firewall{}, err
	}

	checked, err := checkRules(rules)
	if err != nil {
		return Firewall{}, err
	}

	if err := s.repo.replaceRules(ctx, f.ID, checked, s.now()); err != nil {
		return Firewall{}, translate(err)
	}

	f.Rules = checked
	return f, nil
}

func (s *service) remove(ctx context.Context, nameOrID string) error {
	f, err := s.resolve(ctx, nameOrID)
	if err != nil {
		return err
	}
	if err := s.repo.delete(ctx, f.ID); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) exists(ctx context.Context, id string) (bool, error) {
	if _, err := s.repo.byID(ctx, id); errors.Is(err, errNotFound) {
		return false, nil
	} else if err != nil {
		return false, translate(err)
	}
	return true, nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("firewall_not_found", "no firewall with that name or id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("firewall_name_taken", "a firewall with that name already exists")
	default:
		return err
	}
}
