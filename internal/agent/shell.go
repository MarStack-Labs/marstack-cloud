package agent

import (
	"context"
	"io"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const shellPoll = 2 * time.Second

func (a *Agent) ServeShells(ctx context.Context) {
	ticker := time.NewTicker(shellPoll)
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
			a.takeShell(ctx, nodeID)
		}
	}
}

func (a *Agent) takeShell(ctx context.Context, nodeID string) {
	waiting, found, err := a.client.takeShell(ctx, nodeID)
	if err != nil {
		a.log.Warn("could not ask for a shell to open", "error", err)
		return
	}
	if !found {
		return
	}

	go a.runShell(ctx, nodeID, waiting)
}

func (a *Agent) runShell(ctx context.Context, nodeID string, waiting shellView) {
	runtime, known := a.runtimeFor(waiting.Isolation)
	if !known {
		a.log.Warn("no runtime here can open a shell", "isolation", waiting.Isolation)
		return
	}

	sheller, able := runtime.(workload.Sheller)
	if !able {
		a.log.Warn("this runtime cannot open a shell", "isolation", waiting.Isolation)
		return
	}

	inner, stop := context.WithCancel(ctx)
	defer stop()

	down, err := a.client.shellInput(inner, nodeID, waiting.ID)
	if err != nil {
		a.log.Warn("could not open the input stream", "session", waiting.ID, "error", err)
		return
	}
	defer down.Close()

	reader, writer := io.Pipe()
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		if err := a.client.shellOutput(inner, nodeID, waiting.ID, reader); err != nil {
			a.log.Warn("could not send the shell output", "session", waiting.ID,
				"error", err)
		}
	}()

	err = sheller.Shell(inner, waiting.InstanceID, waiting.Command, down, writer)
	writer.CloseWithError(err)

	<-finished
	a.log.Info("shell closed", "session", waiting.ID, "instance", waiting.InstanceID)
}
