package logs

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

type Instances interface {
	NodeOf(ctx context.Context, instanceID string) (nodeID, projectID string, err error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	instances Instances
	now       clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) report(ctx context.Context, params ReportParams) error {
	if s.instances == nil {
		return nil
	}

	nodeID, _, err := s.instances.NodeOf(ctx, params.InstanceID)
	if err != nil {
		return err
	}
	if nodeID != params.NodeID {
		return fault.NotFound("instance_not_here",
			"that instance is not placed on this node")
	}

	if len(params.Texts) > MaxLinesPerReport {
		return fault.Invalid("too_many_lines", fmt.Sprintf(
			"a report carries at most %d lines, and %d were sent",
			MaxLinesPerReport, len(params.Texts)))
	}

	texts := make([]string, 0, len(params.Texts))
	for _, text := range params.Texts {
		texts = append(texts, clip(text))
	}
	if len(texts) == 0 {
		return nil
	}

	return s.repo.append(ctx, params.InstanceID, texts, s.now())
}

func clip(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	if len(text) <= MaxLineBytes {
		return text
	}

	cut := text[:MaxLineBytes]
	for !utf8.ValidString(cut) && len(cut) > 0 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func (s *service) tailIn(ctx context.Context, instanceID, projectID string,
	limit int) ([]Line, error) {
	if s.instances == nil {
		return nil, fault.NotFound("instance_not_found", "no instance with that id exists")
	}

	_, owner, err := s.instances.NodeOf(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if owner != projectID {
		return nil, fault.NotFound("instance_not_found", "no instance with that id exists")
	}

	switch {
	case limit <= 0:
		limit = DefaultTail
	case limit > MaxTail:
		limit = MaxTail
	}
	return s.repo.tail(ctx, instanceID, limit)
}

func (s *service) forget(ctx context.Context, instanceID string) error {
	return s.repo.forget(ctx, instanceID)
}
