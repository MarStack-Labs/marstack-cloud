package balancer

import (
	"context"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/certs"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
)

const CertificateSweep = 10 * time.Minute

func (s *service) sweepCertificates(ctx context.Context) (int, error) {
	if s.events == nil {
		return 0, nil
	}

	known, err := s.repo.all(ctx)
	if err != nil {
		return 0, translate(err)
	}

	held := make(map[string]bool, len(known))
	told := 0

	for _, b := range known {
		held[b.ID] = true
		if !b.TLS.Present() {
			continue
		}

		state, left, readable := certs.Life(b.TLS.ExpiresAt, s.now())
		if !readable || state == certs.Fresh {
			s.noteExpiry(b.ID, "")
			continue
		}
		if !s.noteExpiry(b.ID, state) {
			continue
		}

		entry := events.Entry{
			ProjectID: b.ProjectID,
			Subject:   b.ID,
			Kind:      "certificate." + state,
			Severity:  events.Warn,
			Message: "balancer " + b.Name + " on port " + portOf(b) + ": the certificate for " +
				subjectOf(b) + " expires in " + interval.Human(left),
		}
		if state == certs.Expired {
			entry.Severity = events.Error
			entry.Message = "balancer " + b.Name + " on port " + portOf(b) +
				": the certificate for " + subjectOf(b) + " expired " +
				interval.Human(-left) + " ago, and the port answers nothing a client will trust"
		}

		s.events.Record(ctx, entry)
		told++
	}

	s.forgetExpiry(held)
	return told, nil
}

func subjectOf(b Balancer) string {
	if b.TLS.Subject == "" {
		return "this balancer"
	}
	return b.TLS.Subject
}

func portOf(b Balancer) string {
	return strconv.Itoa(b.ListenPort) + "/" + b.Protocol
}

func (s *service) noteExpiry(id, state string) bool {
	s.expiryMu.Lock()
	defer s.expiryMu.Unlock()

	if state == "" {
		delete(s.expiry, id)
		return false
	}
	if s.expiry[id] == state {
		return false
	}
	s.expiry[id] = state
	return true
}

func (s *service) forgetExpiry(held map[string]bool) {
	s.expiryMu.Lock()
	defer s.expiryMu.Unlock()

	for id := range s.expiry {
		if !held[id] {
			delete(s.expiry, id)
		}
	}
}
