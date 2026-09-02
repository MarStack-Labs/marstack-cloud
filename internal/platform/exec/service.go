package exec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
)

type Target struct {
	InstanceID string
	NodeID     string
	Isolation  string
	Observed   string
}

type Instances interface {
	TargetFor(ctx context.Context, ref, projectID string) (Target, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	instances Instances
	log       *slog.Logger
	now       clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
}

func checkCommand(command []string) error {
	if len(command) == 0 {
		return fault.Invalid("invalid_command", "name a command to run")
	}
	if len(command) > MaxCommandParts {
		return fault.Invalid("invalid_command", fmt.Sprintf(
			"a command takes at most %d parts", MaxCommandParts))
	}
	for _, part := range command {
		if len(part) > MaxPartLength {
			return fault.Invalid("invalid_command", fmt.Sprintf(
				"each part of a command is at most %d characters", MaxPartLength))
		}
		if strings.ContainsRune(part, 0) {
			return fault.Invalid("invalid_command",
				"a command cannot carry a zero byte")
		}
	}
	return nil
}

func parseTimeout(text string) (time.Duration, error) {
	if text == "" {
		return DefaultTimeout, nil
	}

	after, err := interval.Parse(text)
	if err != nil {
		return 0, fault.Invalid("invalid_timeout", err.Error())
	}
	if after < MinTimeout || after > MaxTimeout {
		return 0, fault.Invalid("invalid_timeout",
			"a timeout must be between "+MinTimeout.String()+" and "+MaxTimeout.String())
	}
	return after, nil
}

func (s *service) start(ctx context.Context, params StartParams) (Run, error) {
	if s.instances == nil {
		return Run{}, fault.NotFound("instance_not_found",
			"no instance with that name or id exists")
	}
	if err := checkCommand(params.Command); err != nil {
		return Run{}, err
	}

	after, err := parseTimeout(params.Timeout)
	if err != nil {
		return Run{}, err
	}

	target, err := s.instances.TargetFor(ctx, params.InstanceRef, params.ProjectID)
	if err != nil {
		return Run{}, err
	}
	if target.Isolation != "container" {
		return Run{}, fault.Conflict("no_way_in",
			"only a container can be entered from the node. A vm, microvm or sandbox runs "+
				"its own kernel, so getting inside needs something running in the guest: "+
				"use marstack console on the node instead")
	}
	if target.NodeID == "" {
		return Run{}, fault.Conflict("instance_not_placed",
			"this instance is not on a node, so there is nothing to enter")
	}
	if target.Observed != "running" {
		return Run{}, fault.Conflict("instance_not_running",
			"this instance is "+target.Observed+", and a command needs a running workload "+
				"to run inside")
	}

	waiting, err := s.repo.countWaitingIn(ctx, params.ProjectID)
	if err != nil {
		return Run{}, err
	}
	if waiting >= MaxWaitingPerProject {
		return Run{}, fault.TooMany("too_many_waiting", fmt.Sprintf(
			"%d commands are already waiting to run in this project, and a queue nobody "+
				"drains is just a way to fill the database", waiting))
	}

	run := Run{
		ID:         ids.New("ex"),
		ProjectID:  params.ProjectID,
		InstanceID: target.InstanceID,
		NodeID:     target.NodeID,
		Isolation:  target.Isolation,
		Command:    params.Command,
		Timeout:    after,
		State:      StateWaiting,
		CreatedAt:  s.now(),
	}

	if err := s.repo.insert(ctx, run); err != nil {
		return Run{}, err
	}
	if err := s.repo.trim(ctx, target.InstanceID, KeepPerInstance); err != nil {
		s.log.Warn("could not trim old commands", "instance", target.InstanceID,
			"error", err)
	}
	return run, nil
}

func (s *service) readIn(ctx context.Context, id, projectID string) (Run, error) {
	run, err := s.repo.byID(ctx, id)
	if errors.Is(err, errNotFound) || (err == nil && run.ProjectID != projectID) {
		return Run{}, fault.NotFound("exec_not_found", "no command with that id exists")
	}
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *service) listIn(ctx context.Context, projectID string, limit int) ([]Run, error) {
	return s.repo.listIn(ctx, projectID, limit)
}

func (s *service) take(ctx context.Context, nodeID string) (Run, bool, error) {
	run, err := s.repo.nextWaitingOn(ctx, nodeID)
	if errors.Is(err, errNotFound) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}

	if err := s.repo.setTaken(ctx, run.ID, s.now()); err != nil {
		return Run{}, false, err
	}
	run.State = StateRunning
	return run, true, nil
}

func (s *service) finish(ctx context.Context, nodeID, id string, result Result) error {
	run, err := s.repo.byID(ctx, id)
	if errors.Is(err, errNotFound) {
		return fault.NotFound("exec_not_found", "no command with that id exists")
	}
	if err != nil {
		return err
	}
	if run.NodeID != nodeID {
		return fault.NotFound("exec_not_here",
			"that command was not given to this node")
	}
	if run.State == StateDone {
		return nil
	}

	output, truncated := clip(result.Output)
	if result.Truncated {
		truncated = true
	}

	return s.repo.setDone(ctx, id, output, truncated, result.ExitCode,
		clipMessage(result.Message), s.now())
}

func clip(output string) (string, bool) {
	output = strings.ReplaceAll(output, "\x00", "")
	if len(output) <= MaxOutputBytes {
		return output, false
	}

	cut := output[len(output)-MaxOutputBytes:]
	for !utf8.ValidString(cut) && len(cut) > 0 {
		cut = cut[1:]
	}
	return cut, true
}

func clipMessage(message string) string {
	if len(message) <= MaxPartLength {
		return message
	}
	return message[:MaxPartLength]
}

func (s *service) sweep(ctx context.Context) (int, error) {
	return s.repo.loseAbandoned(ctx, s.now().Add(-Abandoned), s.now())
}
