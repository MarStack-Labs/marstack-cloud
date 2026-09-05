package acme

import (
	"context"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/acme"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/certs"
)

func (s *service) sweep(ctx context.Context) (int, error) {
	if !s.enabled() {
		return 0, nil
	}

	held, err := s.repo.all(ctx)
	if err != nil {
		return 0, err
	}

	moved := 0
	for _, one := range held {
		switch one.State {
		case StatePending:
			if s.advance(ctx, one) {
				moved++
			}
		case StateIssued:
			if s.due(one) && s.restart(ctx, one) {
				moved++
			}
		}
	}
	return moved, nil
}

func (s *service) due(held Order) bool {
	state, _, readable := certs.Life(held.ExpiresAt, s.now().Add(RenewBefore))
	return readable && state != certs.Fresh
}

func (s *service) restart(ctx context.Context, held Order) bool {
	held.State = StatePending
	held.Message = "renewing"
	held.OrderURL = ""
	held.KeyPEM = ""
	held.Challenges = nil
	held.UpdatedAt = s.now()

	if err := s.repo.save(ctx, held); err != nil {
		s.log.Warn("could not begin a renewal", "balancer", held.BalancerID, "error", err)
		return false
	}
	s.log.Info("renewing a certificate", "balancer", held.BalancerID,
		"names", strings.Join(held.Names, ","))
	return true
}

func (s *service) connect(ctx context.Context) (*acme.Client, error) {
	if s.client != nil {
		return s.client, nil
	}

	client := acme.New(s.directory, nil)

	key, url, err := s.repo.account(ctx)
	if err != nil {
		return nil, err
	}

	if key != "" && url != "" {
		if err := client.UseAccount(acme.Account{Key: key, URL: url}); err != nil {
			return nil, err
		}
		s.client = client
		return client, nil
	}

	account, err := client.Register(ctx, s.contact)
	if err != nil {
		return nil, err
	}
	if err := s.repo.saveAccount(ctx, account.Key, account.URL); err != nil {
		return nil, err
	}

	s.log.Info("registered with the certificate authority", "directory", s.directory)
	s.client = client
	return client, nil
}

func (s *service) advance(ctx context.Context, held Order) bool {
	client, err := s.connect(ctx)
	if err != nil {
		s.fail(ctx, held, "could not reach the certificate authority: "+err.Error())
		return true
	}

	if held.OrderURL == "" {
		return s.open(ctx, client, held)
	}
	if !accepted(held) {
		return s.accept(ctx, client, held)
	}
	return s.collect(ctx, client, held)
}

func accepted(held Order) bool {
	for _, one := range held.Challenges {
		if !one.Accepted {
			return false
		}
	}
	return len(held.Challenges) > 0
}

func (s *service) open(ctx context.Context, client *acme.Client, held Order) bool {
	order, err := client.Order(ctx, held.Names)
	if err != nil {
		s.fail(ctx, held, "the authority refused the order: "+err.Error())
		return true
	}

	held.Challenges = held.Challenges[:0]
	for _, one := range order.Challenges {
		authorization, err := client.KeyAuthorization(one.Token)
		if err != nil {
			s.fail(ctx, held, err.Error())
			return true
		}
		held.Challenges = append(held.Challenges, Challenge{
			Name:          one.Name,
			Token:         one.Token,
			Authorization: authorization,
			URL:           one.URL,
		})
	}

	held.OrderURL = order.URL
	held.Message = "waiting for the nodes to serve the challenge"
	held.UpdatedAt = s.now()

	if err := s.repo.save(ctx, held); err != nil {
		s.log.Warn("could not record an order", "balancer", held.BalancerID, "error", err)
		return false
	}
	return true
}

func (s *service) accept(ctx context.Context, client *acme.Client, held Order) bool {
	if s.now().Sub(held.UpdatedAt) < ChallengeGrace {
		return false
	}

	for i := range held.Challenges {
		if held.Challenges[i].Accepted {
			continue
		}
		if err := client.Accept(ctx, held.Challenges[i].URL); err != nil {
			s.fail(ctx, held, "the authority would not take the challenge: "+err.Error())
			return true
		}
		held.Challenges[i].Accepted = true
	}

	held.Message = "the authority is checking the challenge"
	held.UpdatedAt = s.now()

	if err := s.repo.save(ctx, held); err != nil {
		s.log.Warn("could not record the challenge", "balancer", held.BalancerID, "error", err)
	}
	return true
}

func (s *service) collect(ctx context.Context, client *acme.Client, held Order) bool {
	order, err := client.Refresh(ctx, held.OrderURL)
	if err != nil {
		s.fail(ctx, held, "could not read the order back: "+err.Error())
		return true
	}

	switch order.Status {
	case acme.StatusInvalid:
		s.fail(ctx, held,
			"the authority could not reach http://"+held.Names[0]+
				"/.well-known/acme-challenge/ on this platform")
		return true

	case acme.StatusReady:
		key, _, err := client.Finalize(ctx, order, held.Names)
		if err != nil {
			s.fail(ctx, held, "the authority refused the request: "+err.Error())
			return true
		}
		held.KeyPEM = key
		held.Message = "the authority is signing"
		held.UpdatedAt = s.now()
		if err := s.repo.save(ctx, held); err != nil {
			s.log.Warn("could not record the key", "balancer", held.BalancerID, "error", err)
		}
		return true

	case acme.StatusValid:
		if held.KeyPEM == "" {
			s.fail(ctx, held, "the order was issued but this platform lost the private key")
			return true
		}
		chain, err := client.Download(ctx, order.Certificate)
		if err != nil {
			s.fail(ctx, held, "could not fetch the certificate: "+err.Error())
			return true
		}
		if err := s.attach(ctx, held, chain, held.KeyPEM); err != nil {
			s.fail(ctx, held, err.Error())
		}
		return true
	}

	if s.now().Sub(held.UpdatedAt) > OrderLimit {
		s.fail(ctx, held, "the order was still "+order.Status+" after "+OrderLimit.String())
		return true
	}
	return false
}
