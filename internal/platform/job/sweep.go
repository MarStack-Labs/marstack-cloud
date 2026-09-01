package job

import (
	"context"
	"strconv"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
)

func (s *service) sweep(ctx context.Context) (int, int, error) {
	if s.workloads == nil {
		return 0, 0, nil
	}

	jobs, err := s.repo.all(ctx)
	if err != nil {
		return 0, 0, translate(err)
	}

	var started, finished int
	for _, job := range jobs {
		done, err := s.advance(ctx, job)
		if err != nil {
			s.log.Warn("could not advance a job", "job", job.Name, "error", err)
			continue
		}
		finished += done

		if err := s.retain(ctx, job); err != nil {
			s.log.Warn("could not prune a job's runs", "job", job.Name, "error", err)
		}
	}

	fired, err := s.fireDue(ctx)
	if err != nil {
		return started, finished, err
	}
	return fired, finished, nil
}

func (s *service) advance(ctx context.Context, job Job) (int, error) {
	running := active(job.Runs)
	if running == nil {
		return 0, nil
	}
	if running.InstanceID == "" {
		return 0, nil
	}

	state, exitCode, message, err := s.workloads.StateOf(ctx, running.InstanceID)
	if err != nil {
		return 0, s.finish(ctx, job, *running, RunFailed, nil,
			"the workload of this run no longer exists")
	}

	switch {
	case state == "pending":
		return 0, nil
	case state == "running":
		if running.State == RunRunning {
			return 0, nil
		}
		return 0, s.repo.setRunState(ctx, running.ID, RunRunning, nil, "")
	case succeeded(state, exitCode):
		return 1, s.finish(ctx, job, *running, RunSucceeded, exitCode, message)
	}

	if running.Attempt <= job.Retries {
		if err := s.finish(ctx, job, *running, RunFailed, exitCode, message); err != nil {
			return 0, err
		}
		_, err := s.startRun(ctx, job, running.Attempt+1)
		return 1, err
	}
	return 1, s.finish(ctx, job, *running, RunFailed, exitCode, message)
}

func succeeded(state string, exitCode *int) bool {
	return state == "stopped" && exitCode != nil && *exitCode == 0
}

func (s *service) finish(ctx context.Context, job Job, run Run, state string,
	exitCode *int, message string) error {
	if run.InstanceID != "" {
		if err := s.workloads.Delete(ctx, job.ProjectID, run.InstanceID); err != nil {
			s.log.Warn("could not clear the workload of a finished run",
				"job", job.Name, "run", run.ID, "error", err)
		}
	}

	if err := s.repo.endRun(ctx, run.ID, state, exitCode, message, s.now()); err != nil {
		return err
	}

	kind, severity := "job.run_succeeded", events.Info
	if state == RunFailed {
		kind, severity = "job.run_failed", events.Warn
	}
	s.note(ctx, job, events.Entry{
		Kind:    kind,
		Subject: run.ID,
		Message: job.Name + " run " + run.ID + " " + state + " on attempt " +
			strconv.Itoa(run.Attempt),
		Severity: severity,
	})
	return nil
}

func (s *service) fireDue(ctx context.Context) (int, error) {
	due, err := s.repo.due(ctx, s.now(), MaxFirePerPass)
	if err != nil {
		return 0, translate(err)
	}

	fired := 0
	for _, job := range due {
		next := s.now().Add(job.Every)
		if err := s.repo.setNextAt(ctx, job.ID, next, s.now()); err != nil {
			s.log.Warn("could not move a job's next run", "job", job.Name, "error", err)
			continue
		}

		if active(job.Runs) != nil {
			s.note(ctx, job, events.Entry{
				Kind:     "job.run_skipped",
				Subject:  job.ID,
				Message:  job.Name + " was due but its previous run is still going",
				Severity: events.Warn,
			})
			continue
		}

		if _, err := s.startRun(ctx, job, 1); err != nil {
			s.log.Warn("could not start a scheduled run", "job", job.Name, "error", err)
			continue
		}
		fired++
	}
	return fired, nil
}

func (s *service) retain(ctx context.Context, job Job) error {
	settled := make([]Run, 0, len(job.Runs))
	for _, run := range job.Runs {
		if run.State == RunSucceeded || run.State == RunFailed {
			settled = append(settled, run)
		}
	}
	if len(settled) <= job.Keep {
		return nil
	}

	for _, run := range settled[:len(settled)-job.Keep] {
		if err := s.repo.deleteRun(ctx, run.ID); err != nil {
			return err
		}
	}
	return nil
}
