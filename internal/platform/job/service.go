package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Workloads interface {
	Create(ctx context.Context, workload Workload) (string, error)
	Delete(ctx context.Context, projectID, instanceID string) error
	StateOf(ctx context.Context, instanceID string) (state string, exitCode *int,
		message string, err error)
}

type Registry interface {
	ExistsIn(ctx context.Context, id, projectID string) (bool, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	workloads Workloads
	networks  Registry
	firewalls Registry
	events    events.Recorder
	sealing   *sealed.Keyring
	log       *slog.Logger
	now       clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Job, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Job{}, err
	}
	if len(params.Name) > MaxNameLength {
		return Job{}, fault.Invalid("invalid_name", fmt.Sprintf(
			"a job name must be at most %d characters, because every run name is built "+
				"from it", MaxNameLength))
	}
	if err := checkTemplate(params.Template); err != nil {
		return Job{}, err
	}
	if err := checkEnv(params.Env); err != nil {
		return Job{}, err
	}
	if len(params.Files) > MaxFiles {
		return Job{}, fault.Invalid("invalid_files", fmt.Sprintf(
			"a job carries at most %d files, and %d were given", MaxFiles, len(params.Files)))
	}
	if err := s.checkReferences(ctx, params.ProjectID, params.Template); err != nil {
		return Job{}, err
	}

	every, err := parseEvery(params.Every)
	if err != nil {
		return Job{}, err
	}
	if params.Retries < MinRetries || params.Retries > MaxRetries {
		return Job{}, fault.Invalid("invalid_retries", fmt.Sprintf(
			"retries must be between %d and %d", MinRetries, MaxRetries))
	}
	if params.Keep == 0 {
		params.Keep = 10
	}
	if params.Keep < MinKeep || params.Keep > MaxKeep {
		return Job{}, fault.Invalid("invalid_keep", fmt.Sprintf(
			"keep must be between %d and %d", MinKeep, MaxKeep))
	}

	envSealed, keyID, err := sealEnv(params.Env, s.sealing)
	if err != nil {
		return Job{}, err
	}
	filesSealed, err := sealFiles(params.Files, s.sealing)
	if err != nil {
		return Job{}, err
	}
	if filesSealed != "" && keyID == "" {
		keyID = s.sealing.ActiveID()
	}

	params.Template.EnvNames = namesOf(params.Env)
	params.Template.FilePaths = pathsOf(params.Files)

	at := s.now()
	job := Job{
		ID:          ids.New("job"),
		ProjectID:   params.ProjectID,
		Name:        params.Name,
		Template:    params.Template,
		EnvSealed:   envSealed,
		FilesSealed: filesSealed,
		SealKeyID:   keyID,
		Every:       every,
		Retries:     params.Retries,
		Keep:        params.Keep,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
	if every > 0 {
		job.NextAt = at.Add(every)
	}

	if err := s.repo.insert(ctx, job); err != nil {
		return Job{}, translate(err)
	}
	return job, nil
}

func parseEvery(text string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}

	every, err := interval.Parse(text)
	if err != nil {
		return 0, fault.Invalid("invalid_interval", err.Error())
	}
	if every < MinEvery || every > MaxEvery {
		return 0, fault.Invalid("invalid_interval",
			"the interval must be between "+MinEvery.String()+" and "+MaxEvery.String())
	}
	return every, nil
}

func checkTemplate(t Template) error {
	if t.Image == "" {
		return fault.Invalid("invalid_template", "a job needs an image to run")
	}
	if len(t.Command) == 0 {
		return fault.Invalid("invalid_template",
			"a job needs a command: a workload that runs whatever its image starts by "+
				"default never reports finishing on purpose")
	}
	if len(t.NodeSelector) > MaxNodeSelect {
		return fault.Invalid("invalid_node_selector", fmt.Sprintf(
			"a node selector names at most %d labels, and %d were given",
			MaxNodeSelect, len(t.NodeSelector)))
	}
	for key, value := range t.NodeSelector {
		if err := validate.Name("node_selector_key", key); err != nil {
			return err
		}
		if err := validate.Name("node_selector_value", value); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) checkReferences(ctx context.Context, projectID string, t Template) error {
	named := []struct {
		registry Registry
		id       string
	}{
		{s.networks, t.NetworkID},
		{s.firewalls, t.FirewallID},
	}

	for _, one := range named {
		if one.registry == nil || one.id == "" {
			continue
		}
		known, err := one.registry.ExistsIn(ctx, one.id, projectID)
		if err != nil {
			return err
		}
		if !known {
			return fault.Invalid("unknown_reference", "nothing with id "+one.id+
				" exists in this project, and every run would fail the same way")
		}
	}
	return nil
}

func (s *service) find(ctx context.Context, projectID, id string) (Job, error) {
	job, err := s.repo.byID(ctx, projectID, id)
	if errors.Is(err, errNotFound) {
		job, err = s.repo.byName(ctx, projectID, id)
	}
	if err != nil {
		return Job{}, translate(err)
	}
	return job, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Job, error) {
	jobs, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return jobs, nil
}

func (s *service) setPaused(ctx context.Context, projectID, id string, paused bool) (Job, error) {
	job, err := s.find(ctx, projectID, id)
	if err != nil {
		return Job{}, err
	}
	if err := s.repo.setPaused(ctx, job.ID, paused, s.now()); err != nil {
		return Job{}, translate(err)
	}
	return s.find(ctx, projectID, job.ID)
}

func (s *service) remove(ctx context.Context, projectID, id string) error {
	job, err := s.find(ctx, projectID, id)
	if err != nil {
		return err
	}

	for _, run := range job.Runs {
		if s.workloads == nil || run.InstanceID == "" {
			continue
		}
		if err := s.workloads.Delete(ctx, job.ProjectID, run.InstanceID); err != nil {
			return fault.Internal(fmt.Errorf(
				"job %s could not delete the workload of run %s, so nothing was removed: %w",
				job.Name, run.ID, err))
		}
	}

	if err := s.repo.delete(ctx, job.ID); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) trigger(ctx context.Context, projectID, id string) (Run, error) {
	job, err := s.find(ctx, projectID, id)
	if err != nil {
		return Run{}, err
	}
	if active(job.Runs) != nil {
		return Run{}, fault.Conflict("run_in_flight",
			"a run of this job is still going, and two at once would race each other")
	}
	return s.startRun(ctx, job, 1)
}

func active(runs []Run) *Run {
	for i := range runs {
		if runs[i].State == RunPending || runs[i].State == RunRunning {
			return &runs[i]
		}
	}
	return nil
}

func (s *service) startRun(ctx context.Context, job Job, attempt int) (Run, error) {
	if s.workloads == nil {
		return Run{}, fault.Internal(errors.New("this control plane cannot make workloads"))
	}

	env, err := openEnv(job.EnvSealed, job.SealKeyID, s.sealing)
	if err != nil {
		return Run{}, err
	}

	var files []File
	if job.FilesSealed != "" {
		files, err = openFiles(job.FilesSealed, job.SealKeyID, s.sealing)
		if err != nil {
			return Run{}, err
		}
	}

	run := Run{
		ID:        ids.New("run"),
		JobID:     job.ID,
		Attempt:   attempt,
		State:     RunPending,
		StartedAt: s.now(),
	}

	instanceID, err := s.workloads.Create(ctx, Workload{
		ProjectID: job.ProjectID,
		Name:      runName(job.Name, attempt),
		Template:  job.Template,
		Env:       env,
		Files:     files,
	})
	if err != nil {
		run.State = RunFailed
		run.Message = err.Error()
		run.EndedAt = run.StartedAt
		if insertErr := s.repo.insertRun(ctx, run); insertErr != nil {
			return Run{}, translate(insertErr)
		}
		s.note(ctx, job, events.Entry{
			Kind:     "job.run_failed",
			Subject:  run.ID,
			Message:  job.Name + " could not start a run: " + err.Error(),
			Severity: events.Error,
		})
		return run, nil
	}

	run.InstanceID = instanceID
	if err := s.repo.insertRun(ctx, run); err != nil {
		return Run{}, translate(err)
	}

	s.note(ctx, job, events.Entry{
		Kind:    "job.run_started",
		Subject: run.ID,
		Message: job.Name + " started run " + run.ID + " (attempt " +
			strconv.Itoa(attempt) + ")",
		Severity: events.Info,
	})
	return run, nil
}

func runName(job string, attempt int) string {
	suffix := ids.New("r")
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return job + "-" + strconv.Itoa(attempt) + "-" + suffix
}

func (s *service) note(ctx context.Context, job Job, entry events.Entry) {
	if s.events == nil {
		return
	}
	entry.ProjectID = job.ProjectID
	s.events.Record(ctx, entry)
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("job_not_found", "no job with that name or id exists")
	case errors.Is(err, errNameUsed):
		return fault.Conflict("job_name_taken",
			"a job with that name already exists in this project")
	default:
		return err
	}
}
