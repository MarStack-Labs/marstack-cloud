package event

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type clock func() time.Time

type service struct {
	repo *repository
	log  *slog.Logger
	now  clock

	mu   sync.Mutex
	seen int
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
}

func (s *service) record(ctx context.Context, entry events.Entry) {
	if entry.Kind == "" {
		return
	}

	stored := Entry{
		At:        s.now(),
		ProjectID: entry.ProjectID,
		Kind:      clip(entry.Kind, MaxKind),
		Subject:   entry.Subject,
		NodeID:    entry.NodeID,
		Message:   clip(entry.Message, MaxMessage),
		Severity:  severityOf(entry.Severity),
	}

	if err := s.repo.insert(ctx, stored); err != nil {
		s.log.Warn("could not record an event",
			"kind", stored.Kind, "subject", stored.Subject, "error", err)
		return
	}

	if !s.dueForPrune() {
		return
	}
	if err := s.repo.prune(ctx, Retain); err != nil {
		s.log.Warn("could not prune events", "error", err)
	}
}

func (s *service) dueForPrune() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seen++
	if s.seen < PruneEvery {
		return false
	}
	s.seen = 0
	return true
}

func severityOf(severity events.Severity) string {
	switch severity {
	case events.Warn, events.Error:
		return string(severity)
	default:
		return string(events.Info)
	}
}

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func (s *service) list(ctx context.Context, filter Filter) ([]Entry, error) {
	if filter.Limit <= 0 {
		filter.Limit = DefaultLimit
	}
	if filter.Limit > MaxLimit {
		filter.Limit = MaxLimit
	}
	if filter.Severity != "" {
		if err := validate.OneOf("severity", filter.Severity,
			string(events.Info), string(events.Warn), string(events.Error)); err != nil {
			return nil, err
		}
	}
	if filter.ProjectID == "" {
		return nil, nil
	}
	return s.repo.list(ctx, filter)
}

func (s *service) since(ctx context.Context, afterID int64, limit int) ([]Entry, error) {
	if limit <= 0 || limit > MaxLimit {
		limit = MaxLimit
	}
	return s.repo.since(ctx, afterID, limit)
}

func (s *service) newestID(ctx context.Context) (int64, error) {
	return s.repo.newestID(ctx)
}

func (s *service) count(ctx context.Context) (int, error) {
	return s.repo.count(ctx)
}
