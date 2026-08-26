package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const cacheFileName = "desired-state.json"

type cachedState struct {
	NodeID    string          `json:"node_id"`
	SavedAt   time.Time       `json:"saved_at"`
	Instances []instanceView  `json:"instances"`
	Networks  []networkView   `json:"networks"`
	Records   []dnsRecordView `json:"records"`
	Nodes     []nodeView      `json:"nodes"`
	Volumes   []volumeView    `json:"volumes"`
	Forwards  []forwardView   `json:"forwards"`
	Firewalls []firewallView  `json:"firewalls"`
}

func (a *Agent) cachePath() string {
	return filepath.Join(a.cfg.StateDir, cacheFileName)
}

func (a *Agent) saveState(state cachedState) {
	if a.cfg.StateDir == "" {
		return
	}

	state.NodeID = a.currentNodeID()
	state.SavedAt = a.now()

	encoded, err := json.Marshal(state)
	if err != nil {
		a.log.Warn("could not encode the desired state", "error", err)
		return
	}

	if err := os.MkdirAll(a.cfg.StateDir, 0o750); err != nil {
		a.log.Warn("could not create the state directory", "error", err)
		return
	}

	partial := a.cachePath() + ".partial"
	if err := os.WriteFile(partial, encoded, 0o600); err != nil {
		a.log.Warn("could not write the desired state", "error", err)
		return
	}
	if err := os.Rename(partial, a.cachePath()); err != nil {
		a.log.Warn("could not commit the desired state", "error", err)
	}
}

func (a *Agent) loadState() (cachedState, error) {
	if a.cfg.StateDir == "" {
		return cachedState{}, fmt.Errorf("no state directory is configured")
	}

	raw, err := os.ReadFile(a.cachePath())
	if err != nil {
		return cachedState{}, err
	}

	var state cachedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return cachedState{}, fmt.Errorf("decode the cached desired state: %w", err)
	}
	if state.NodeID == "" {
		return cachedState{}, fmt.Errorf("the cached desired state has no node id")
	}
	return state, nil
}

func (a *Agent) reconcileFromCache(ctx context.Context) {
	silence, cut := a.fenced()
	if cut {
		a.fence(ctx, silence)
	}

	state, err := a.loadState()
	if err != nil {
		a.log.Warn("no usable cached desired state, waiting for the control plane", "error", err)
		return
	}

	a.log.Warn("reconciling from the cached desired state",
		"node_id", state.NodeID,
		"saved_at", state.SavedAt.Format(time.RFC3339),
		"instances", len(state.Instances),
	)

	a.setNodeID(state.NodeID)

	if cut {
		state.Instances = unmovable(state.Instances)
		a.log.Warn("only workloads the scheduler never moves are started from cache",
			"instances", len(state.Instances))
	}

	a.applyDesired(ctx, state, false)
}

func unmovable(assigned []instanceView) []instanceView {
	kept := make([]instanceView, 0, len(assigned))
	for _, in := range assigned {
		if in.Isolation == fenceIsolation {
			continue
		}
		kept = append(kept, in)
	}
	return kept
}
