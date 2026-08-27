package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	probeTimeout  = 3 * time.Second
	probeBodyCap  = 4 * 1024
	checkKindNone = "none"
	checkKindTCP  = "tcp"
	checkKindHTTP = "http"
)

type probeState struct {
	passes    int
	failures  int
	verdict   bool
	settled   bool
	lastError string
}

type probeTarget struct {
	balancerID string
	instanceID string
	address    string
	port       int
	kind       string
	path       string
	rise       int
	fall       int
}

func (a *Agent) probeStateOf(key string) *probeState {
	a.probesMu.Lock()
	defer a.probesMu.Unlock()

	state, known := a.probes[key]
	if !known {
		state = &probeState{}
		a.probes[key] = state
	}
	return state
}

func (a *Agent) forgetProbes(wanted map[string]bool) {
	a.probesMu.Lock()
	defer a.probesMu.Unlock()

	for key := range a.probes {
		if !wanted[key] {
			delete(a.probes, key)
		}
	}
}

func probeTargets(balancers []balancerView, mine map[string]bool) []probeTarget {
	targets := make([]probeTarget, 0, len(balancers))

	for _, b := range balancers {
		if b.Check == "" || b.Check == checkKindNone {
			continue
		}
		for _, backend := range b.Backends {
			if !mine[backend.InstanceID] || backend.Address == "" {
				continue
			}
			targets = append(targets, probeTarget{
				balancerID: b.ID,
				instanceID: backend.InstanceID,
				address:    backend.Address,
				port:       b.TargetPort,
				kind:       b.Check,
				path:       b.CheckPath,
				rise:       b.Rise,
				fall:       b.Fall,
			})
		}
	}
	return targets
}

func (a *Agent) runProbes(ctx context.Context, balancers []balancerView, assigned []instanceView) {
	mine := make(map[string]bool, len(assigned))
	for _, in := range assigned {
		mine[in.ID] = true
	}

	targets := probeTargets(balancers, mine)

	wanted := make(map[string]bool, len(targets))
	for _, target := range targets {
		wanted[target.balancerID+"/"+target.instanceID] = true
	}
	a.forgetProbes(wanted)

	if len(targets) == 0 {
		return
	}

	reports := make([]healthReportBody, 0, len(targets))
	for _, target := range targets {
		verdict, reason, changed := a.settle(ctx, target)
		if changed {
			a.log.Info("backend health changed", "balancer", target.balancerID,
				"instance", target.instanceID, "healthy", verdict, "reason", reason)
		}
		reports = append(reports, healthReportBody{
			BalancerID: target.balancerID,
			InstanceID: target.instanceID,
			Healthy:    verdict,
			Reason:     reason,
		})
	}

	if err := a.client.reportHealth(ctx, a.currentNodeID(), reports); err != nil {
		a.log.Warn("could not report backend health", "error", err)
	}
}

func (a *Agent) settle(ctx context.Context, target probeTarget) (bool, string, bool) {
	err := probe(ctx, target)
	state := a.probeStateOf(target.balancerID + "/" + target.instanceID)

	a.probesMu.Lock()
	defer a.probesMu.Unlock()

	before, wasSettled := state.verdict, state.settled

	if err == nil {
		state.failures = 0
		state.passes++
	} else {
		state.passes = 0
		state.failures++
		state.lastError = err.Error()
	}

	switch {
	case state.passes >= target.rise:
		state.verdict, state.settled = true, true
	case state.failures >= target.fall:
		state.verdict, state.settled = false, true
	}

	changed := !wasSettled || before != state.verdict
	return state.verdict, reasonFor(state, target), changed
}

func reasonFor(state *probeState, target probeTarget) string {
	of := func(count, needed int) string {
		return strconv.Itoa(count) + " of " + strconv.Itoa(needed)
	}

	switch {
	case !state.settled && state.passes > 0:
		return "passed " + of(state.passes, target.rise) + " needed to come up"
	case !state.settled:
		return "failed " + of(state.failures, target.fall) + " needed to go down: " +
			state.lastError
	case state.verdict && state.failures > 0:
		return "failed " + of(state.failures, target.fall) + " needed to go down, still up: " +
			state.lastError
	case state.verdict:
		return ""
	case state.passes > 0:
		return "passed " + of(state.passes, target.rise) + " needed to come back up"
	default:
		return state.lastError
	}
}

func probe(ctx context.Context, target probeTarget) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if target.kind == checkKindHTTP {
		return probeHTTP(ctx, target)
	}
	return probeTCP(ctx, target)
}

func probeTCP(ctx context.Context, target probeTarget) error {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp",
		net.JoinHostPort(target.address, strconv.Itoa(target.port)))
	if err != nil {
		return fmt.Errorf("tcp connect refused or timed out")
	}
	return conn.Close()
}

func probeHTTP(ctx context.Context, target probeTarget) error {
	url := "http://" + net.JoinHostPort(target.address, strconv.Itoa(target.port)) + target.path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("could not build the probe request")
	}

	res, err := probeClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed or timed out")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, probeBodyCap))

	if res.StatusCode < 200 || res.StatusCode > 399 {
		return fmt.Errorf("http status %d", res.StatusCode)
	}
	return nil
}

var probeClient = &http.Client{
	Timeout: probeTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{Timeout: probeTimeout}).DialContext,
	},
}
