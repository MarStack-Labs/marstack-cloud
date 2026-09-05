package shell

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
)

type Instances interface {
	TargetFor(ctx context.Context, ref, projectID string) (Target, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	instances Instances
	relay     *relay
	log       *slog.Logger
	now       clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, relay: newRelay(), log: log, now: now}
}

func (s *service) start(ctx context.Context, params StartParams) (Session, error) {
	if s.instances == nil {
		return Session{}, fault.NotFound("instance_not_found",
			"no instance with that name or id exists")
	}

	target, err := s.instances.TargetFor(ctx, params.InstanceRef, params.ProjectID)
	if err != nil {
		return Session{}, err
	}
	if target.Isolation != "container" {
		return Session{}, fault.Conflict("no_way_in",
			"only a container can be entered from the node. A vm, microvm or sandbox runs "+
				"its own kernel, so a shell inside needs something running in the guest: "+
				"use marstack console on the node instead")
	}
	if target.NodeID == "" {
		return Session{}, fault.Conflict("instance_not_placed",
			"this instance is not on a node, so there is nothing to enter")
	}
	if target.Observed != "running" {
		return Session{}, fault.Conflict("instance_not_running",
			"this instance is "+target.Observed+", and a shell needs a running workload "+
				"to run inside")
	}

	live, err := s.repo.countLiveIn(ctx, params.ProjectID)
	if err != nil {
		return Session{}, err
	}
	if live >= MaxWaitingPerProject {
		return Session{}, fault.TooMany("too_many_sessions", fmt.Sprintf(
			"%d shells are already open in this project, and each one holds a process on a "+
				"node for as long as it lives", live))
	}

	command := params.Command
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	held := Session{
		ID:         ids.New("sh"),
		ProjectID:  params.ProjectID,
		InstanceID: target.InstanceID,
		NodeID:     target.NodeID,
		Isolation:  target.Isolation,
		Command:    command,
		State:      StateWaiting,
		CreatedAt:  s.now(),
	}

	if err := s.repo.insert(ctx, held); err != nil {
		return Session{}, err
	}

	s.relay.open(held.ID)
	return held, nil
}

func (s *service) readIn(ctx context.Context, id, projectID string) (Session, error) {
	held, err := s.repo.byID(ctx, id)
	if errors.Is(err, errNotFound) || (err == nil && held.ProjectID != projectID) {
		return Session{}, fault.NotFound("session_not_found",
			"no shell session with that id exists")
	}
	return held, err
}

func (s *service) listIn(ctx context.Context, projectID string, limit int) ([]Session, error) {
	return s.repo.listIn(ctx, projectID, limit)
}

func (s *service) take(ctx context.Context, nodeID string) (Session, bool, error) {
	held, err := s.repo.nextWaitingOn(ctx, nodeID)
	if errors.Is(err, errNotFound) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}

	taken, err := s.repo.take(ctx, held.ID, s.now())
	if err != nil || !taken {
		return Session{}, false, err
	}

	held.State = StateAttached
	return held, true, nil
}

func (s *service) close(ctx context.Context, id, message string) error {
	s.relay.shut(id, errors.New("the session is closed"))
	return s.repo.close(ctx, id, message, s.now())
}

func (s *service) heldBy(ctx context.Context, id, nodeID string) (Session, error) {
	held, err := s.repo.byID(ctx, id)
	if errors.Is(err, errNotFound) {
		return Session{}, fault.NotFound("session_not_found",
			"no shell session with that id exists")
	}
	if err != nil {
		return Session{}, err
	}
	if held.NodeID != nodeID {
		return Session{}, fault.NotFound("session_not_here",
			"that session was not given to this node")
	}
	return held, nil
}

func (s *service) ReleaseInstance(ctx context.Context, instanceID string) error {
	return s.repo.deleteInstance(ctx, instanceID)
}

func (s *service) sweep(ctx context.Context) (int, error) {
	held, err := s.repo.stale(ctx, s.now().Add(-Abandoned))
	if err != nil {
		return 0, err
	}

	for _, one := range held {
		if err := s.close(ctx, one.ID, "no node picked it up"); err != nil {
			s.log.Warn("could not close an abandoned session", "session", one.ID,
				"error", err)
			continue
		}
	}
	return len(held), nil
}
