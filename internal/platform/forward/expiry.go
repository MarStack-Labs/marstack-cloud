package forward

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

	for _, f := range known {
		held[f.ID] = true
		if !f.TLS.Present() {
			continue
		}

		state, left, readable := certs.Life(f.TLS.ExpiresAt, s.now())
		if !readable || state == certs.Fresh {
			s.noteExpiry(f.ID, "")
			continue
		}
		if !s.noteExpiry(f.ID, state) {
			continue
		}

		port := strconv.Itoa(f.NodePort) + "/" + f.Protocol
		entry := events.Entry{
			ProjectID: f.ProjectID,
			NodeID:    f.NodeID,
			Subject:   f.ID,
			Kind:      "certificate." + state,
			Severity:  events.Warn,
			Message: "published port " + port + ": the certificate for " + subjectOf(f) +
				" expires in " + interval.Human(left),
		}
		if state == certs.Expired {
			entry.Severity = events.Error
			entry.Message = "published port " + port + ": the certificate for " + subjectOf(f) +
				" expired " + interval.Human(-left) +
				" ago, and the port answers nothing a client will trust"
		}

		s.events.Record(ctx, entry)
		told++
	}

	s.forgetExpiry(held)
	return told, nil
}

func subjectOf(f Forward) string {
	if f.TLS.Subject == "" {
		return "this port"
	}
	return f.TLS.Subject
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
