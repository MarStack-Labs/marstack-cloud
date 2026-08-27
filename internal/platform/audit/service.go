package audit

import (
	"context"
	"time"
)

type clock func() time.Time

type service struct {
	repo  *repository
	now   clock
	every int
	seen  int
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now, every: 200}
}

func (s *service) record(ctx context.Context, record Record) error {
	entry := Entry{
		At:        s.now(),
		Actor:     record.Actor,
		Role:      record.Role,
		ProjectID: record.ProjectID,
		Method:    record.Method,
		Path:      record.Path,
		Status:    record.Status,
		RequestID: record.RequestID,
	}

	if err := s.repo.insert(ctx, entry); err != nil {
		return err
	}

	s.seen++
	if s.seen < s.every {
		return nil
	}
	s.seen = 0

	return s.repo.prune(ctx, Retain)
}

func (s *service) list(ctx context.Context, limit int, projectID string) ([]Entry, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return s.repo.list(ctx, limit, projectID)
}

func (s *service) count(ctx context.Context) (int, error) {
	return s.repo.count(ctx)
}
