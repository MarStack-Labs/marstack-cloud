package agent

import (
	"context"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	maxCommandsPerPass = 4
	commandPoll        = 2 * time.Second
)

func (a *Agent) ServeCommands(ctx context.Context) {
	ticker := time.NewTicker(commandPoll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nodeID := a.currentNodeID()
			if nodeID == "" {
				continue
			}
			a.runCommands(ctx, nodeID)
		}
	}
}

func (a *Agent) runCommands(ctx context.Context, nodeID string) {
	for range maxCommandsPerPass {
		waiting, found, err := a.client.takeCommand(ctx, nodeID)
		if err != nil {
			a.log.Warn("could not ask for a command to run", "error", err)
			return
		}
		if !found {
			return
		}
		a.runOne(ctx, nodeID, waiting)
	}
}

func (a *Agent) runOne(ctx context.Context, nodeID string, waiting commandView) {
	result := commandResult{ExitCode: nil, Message: "no runtime here can enter that workload"}

	runtime, known := a.runtimeFor(waiting.Isolation)
	if known {
		if enterable, able := runtime.(workload.Enterable); able {
			result = a.enter(ctx, enterable, waiting)
		}
	}

	if err := a.client.finishCommand(ctx, nodeID, waiting.ID, result); err != nil {
		a.log.Warn("ran a command but could not report it",
			"exec", waiting.ID, "error", err)
	}
}

func (a *Agent) enter(ctx context.Context, enterable workload.Enterable,
	waiting commandView) commandResult {
	limit := time.Duration(waiting.TimeoutSeconds) * time.Second
	if limit <= 0 {
		limit = 30 * time.Second
	}

	inside, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	entered, err := enterable.Enter(inside, waiting.InstanceID, waiting.Command,
		maxCommandOutput)
	if err != nil {
		return commandResult{Message: err.Error()}
	}

	code := entered.ExitCode
	return commandResult{
		Output:    entered.Output,
		Truncated: entered.Truncated,
		ExitCode:  &code,
		Message:   entered.Message,
	}
}
