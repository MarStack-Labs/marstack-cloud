package alert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Load interface {
	SamplesOf(ctx context.Context) (map[string]Sample, error)
}

type Workloads interface {
	ResolveIn(ctx context.Context, ref, projectID string) (string, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	load      Load
	workloads Workloads
	events    events.Recorder
	log       *slog.Logger
	now       clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Alert, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Alert{}, err
	}
	if err := validate.OneOf("metric", params.Metric, Metrics()...); err != nil {
		return Alert{}, err
	}
	if params.Comparison == "" {
		params.Comparison = Above
	}
	if err := validate.OneOf("comparison", params.Comparison, Comparisons()...); err != nil {
		return Alert{}, err
	}
	if params.Threshold <= 0 || params.Threshold > 100 {
		return Alert{}, fault.Invalid("invalid_threshold",
			"a threshold is a percentage between 0 and 100")
	}

	window := DefaultFor
	if params.For != "" {
		parsed, err := interval.Parse(params.For)
		if err != nil {
			return Alert{}, fault.Invalid("invalid_for", err.Error())
		}
		window = parsed
	}
	if window < MinFor || window > MaxFor {
		return Alert{}, fault.Invalid("invalid_for",
			"an alert must hold for between "+MinFor.String()+" and "+MaxFor.String()+
				": anything shorter fires on a single spike")
	}

	if s.workloads == nil {
		return Alert{}, fault.Unavailable("instances_unavailable",
			"the platform cannot look up a workload")
	}
	instanceID, err := s.workloads.ResolveIn(ctx, params.InstanceID, params.ProjectID)
	if err != nil {
		return Alert{}, err
	}

	count, err := s.repo.countIn(ctx, params.ProjectID)
	if err != nil {
		return Alert{}, err
	}
	if count >= MaxPerProject {
		return Alert{}, fault.Invalid("too_many_alerts", fmt.Sprintf(
			"a project holds at most %d alerts", MaxPerProject))
	}

	at := s.now()
	held := Alert{
		ID:         ids.New("alr"),
		ProjectID:  params.ProjectID,
		Name:       params.Name,
		InstanceID: instanceID,
		Metric:     params.Metric,
		Comparison: params.Comparison,
		Threshold:  params.Threshold,
		For:        window,
		State:      StateQuiet,
		Since:      at,
		Message:    "nothing has been read yet",
		CreatedAt:  at,
		UpdatedAt:  at,
	}

	if err := s.repo.insert(ctx, held); err != nil {
		if errors.Is(err, errNameTaken) {
			return Alert{}, fault.Conflict("name_taken",
				"an alert called "+params.Name+" already exists in this project")
		}
		return Alert{}, err
	}
	return held, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Alert, error) {
	return s.repo.listIn(ctx, projectID)
}

func (s *service) get(ctx context.Context, projectID, ref string) (Alert, error) {
	held, err := s.repo.find(ctx, projectID, ref)
	if errors.Is(err, errNotFound) {
		return Alert{}, fault.NotFound("alert_not_found",
			"no alert with that name or id exists")
	}
	return held, err
}

func (s *service) remove(ctx context.Context, projectID, ref string) error {
	held, err := s.get(ctx, projectID, ref)
	if err != nil {
		return err
	}
	return s.repo.delete(ctx, held.ID)
}

func (s *service) ReleaseInstance(ctx context.Context, instanceID string) error {
	return s.repo.deleteInstance(ctx, instanceID)
}

func (s *service) sweep(ctx context.Context) (int, error) {
	if s.load == nil {
		return 0, nil
	}

	held, err := s.repo.all(ctx)
	if err != nil {
		return 0, err
	}
	if len(held) == 0 {
		return 0, nil
	}

	samples, err := s.load.SamplesOf(ctx)
	if err != nil {
		return 0, err
	}

	now := s.now()
	moved := 0

	for _, one := range held {
		sample, found := samples[one.InstanceID]
		out := decide(one, sample, found, now)

		before := one.State
		one.State = out.State
		one.LastValue = out.Value
		one.UpdatedAt = now
		if out.Message != "" {
			one.Message = out.Message
		}
		if before != out.State {
			one.Since = now
		}

		if err := s.repo.setState(ctx, one); err != nil {
			s.log.Warn("could not record an alert", "alert", one.ID, "error", err)
			continue
		}
		if !out.Changed {
			continue
		}

		s.announce(ctx, one, before)
		moved++
	}
	return moved, nil
}

func (s *service) announce(ctx context.Context, held Alert, before string) {
	if s.events == nil {
		return
	}
	if held.State == StateWarming {
		return
	}

	entry := events.Entry{
		ProjectID: held.ProjectID,
		Subject:   held.InstanceID,
		Kind:      "alert.cleared",
		Severity:  events.Info,
		Message:   held.Name + ": " + held.Message,
	}
	if held.State == StateFiring {
		entry.Kind = "alert.firing"
		entry.Severity = events.Warn
	}
	if held.State == StateQuiet && before != StateFiring {
		return
	}

	s.events.Record(ctx, entry)
}
