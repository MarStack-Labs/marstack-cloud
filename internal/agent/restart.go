package agent

import (
	"time"
)

const (
	minRestartBackoff = 1 * time.Second
	maxRestartBackoff = 60 * time.Second
	stableFor         = 60 * time.Second
)

type restartState struct {
	attempts     int
	nextAttempt  time.Time
	startedAt    time.Time
	haltedByUser bool
	startFailure string
	unstartable  string
}

func (a *Agent) restartStateOf(instanceID string) *restartState {
	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	state, known := a.restarts[instanceID]
	if !known {
		state = &restartState{}
		a.restarts[instanceID] = state
	}
	return state
}

func (a *Agent) noteHaltedByUser(instanceID string) {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	state.attempts = 0
	state.nextAttempt = time.Time{}
	state.haltedByUser = true
}

func (a *Agent) takeHaltedByUser(instanceID string) bool {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	halted := state.haltedByUser
	state.haltedByUser = false
	return halted
}

func (a *Agent) noteStarted(instanceID string) {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	state.startedAt = a.now()
	state.haltedByUser = false
	state.startFailure = ""
}

func (a *Agent) noteStartFailure(instanceID, reason string) {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	state.startFailure = reason
}

func (a *Agent) noteUnstartable(instanceID, reason string) bool {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	first := state.unstartable == ""
	state.unstartable = reason
	return first
}

func (a *Agent) unstartable(instanceID string) (string, bool) {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	return state.unstartable, state.unstartable != ""
}

func (a *Agent) forgetRestarts(assigned []instanceView) {
	keep := make(map[string]bool, len(assigned))
	for _, in := range assigned {
		keep[in.ID] = true
	}

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	for id := range a.restarts {
		if !keep[id] {
			delete(a.restarts, id)
		}
	}
}

func (a *Agent) lastStartFailure(instanceID string) string {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()
	return state.startFailure
}

func (a *Agent) noteRestart(instanceID string) int {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	now := a.now()
	if !state.startedAt.IsZero() && now.Sub(state.startedAt) >= stableFor {
		state.attempts = 0
	}

	state.attempts++
	state.nextAttempt = now.Add(backoffFor(state.attempts))
	return state.attempts
}

func (a *Agent) restartAllowed(instanceID string) (bool, time.Duration) {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()

	wait := state.nextAttempt.Sub(a.now())
	if wait > 0 {
		return false, wait
	}
	return true, 0
}

func (a *Agent) restartAttempts(instanceID string) int {
	state := a.restartStateOf(instanceID)

	a.restartsMu.Lock()
	defer a.restartsMu.Unlock()
	return state.attempts
}

func backoffFor(attempt int) time.Duration {
	backoff := minRestartBackoff
	for range attempt - 1 {
		backoff *= 2
		if backoff >= maxRestartBackoff {
			return maxRestartBackoff
		}
	}
	return backoff
}

func shouldRestart(policy string, exitCode int) bool {
	switch policy {
	case restartAlways:
		return true
	case restartOnFailure:
		return exitCode != 0
	default:
		return false
	}
}
