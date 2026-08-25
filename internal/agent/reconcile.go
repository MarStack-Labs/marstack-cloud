package agent

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	dnsSuffix = "internal"

	restartNever     = "never"
	restartOnFailure = "on-failure"
	restartAlways    = "always"

	desiredRunning = "running"
	desiredStopped = "stopped"

	observedPending = "pending"
	observedRunning = "running"
	observedStopped = "stopped"
	observedFailed  = "failed"
)

func (a *Agent) reconcile(ctx context.Context) {
	if len(a.runtimes) == 0 {
		return
	}

	state, err := a.readDesired(ctx)
	if err != nil {
		a.log.Warn("could not read the desired state", "error", err)
		return
	}

	a.saveState(state)
	a.applyDesired(ctx, state, true)
}

func (a *Agent) readDesired(ctx context.Context) (cachedState, error) {
	nodeID := a.currentNodeID()

	assigned, err := a.client.assignedInstances(ctx, nodeID)
	if err != nil {
		return cachedState{}, fmt.Errorf("assigned instances: %w", err)
	}

	networks, err := a.client.nodeNetworks(ctx, nodeID)
	if err != nil {
		return cachedState{}, fmt.Errorf("node network view: %w", err)
	}

	records, err := a.client.dnsRecords(ctx)
	if err != nil {
		return cachedState{}, fmt.Errorf("dns records: %w", err)
	}

	nodes, err := a.client.nodes(ctx)
	if err != nil {
		return cachedState{}, fmt.Errorf("nodes: %w", err)
	}

	return cachedState{
		Instances: assigned,
		Networks:  networks,
		Records:   records,
		Nodes:     nodes,
	}, nil
}

func (a *Agent) applyDesired(ctx context.Context, state cachedState, report bool) {
	if len(a.runtimes) == 0 {
		return
	}

	a.collectGarbage(ctx, state.Instances, state.Networks)
	a.serveDNS(ctx, state.Networks, state.Records)

	interfaces := a.interfacesByInstance(state.Networks)
	a.applyRoutes(ctx, state.Networks, state.Nodes)
	a.applyFilters(ctx, state.Networks, isolationsOf(state.Instances))

	for _, in := range state.Instances {
		observed, message := a.reconcileOne(ctx, in, interfaces[in.ID])
		restarts := a.restartAttempts(in.ID)

		if !report {
			continue
		}
		if in.ObservedState == observed && in.ObservedMessage == message && in.RestartCount == restarts {
			continue
		}
		if err := a.client.reportStatus(ctx, a.currentNodeID(), in.ID, observed, message, restarts); err != nil {
			a.log.Warn("could not report status", "instance", in.ID, "error", err)
		}
	}
}

func isolationsOf(assigned []instanceView) map[string]string {
	isolations := make(map[string]string, len(assigned))
	for _, in := range assigned {
		isolations[in.ID] = in.Isolation
	}
	return isolations
}

func (a *Agent) collectGarbage(ctx context.Context, assigned []instanceView, networks []networkView) {
	wanted := make(map[string]bool, len(assigned))
	instanceIDs := make([]string, 0, len(assigned))
	for _, in := range assigned {
		wanted[in.ID] = true
		instanceIDs = append(instanceIDs, in.ID)
	}

	for isolation, runtime := range a.runtimes {
		present, err := runtime.List(ctx)
		if err != nil {
			a.log.Warn("could not list local workloads", "isolation", isolation, "error", err)
			continue
		}

		for _, id := range present {
			if wanted[id] {
				continue
			}
			a.log.Info("removing a workload the control plane no longer knows",
				"instance", id, "isolation", isolation)
			if err := runtime.Remove(ctx, id); err != nil {
				a.log.Warn("could not remove an orphaned workload", "instance", id, "error", err)
			}
		}
	}

	if a.datapath == nil {
		return
	}

	bridges := make([]string, 0, len(networks))
	for _, n := range networks {
		bridges = append(bridges, n.Bridge)
	}

	if err := a.datapath.Prune(ctx, workload.Keep{Bridges: bridges, Instances: instanceIDs}); err != nil {
		a.log.Warn("could not prune the datapath", "error", err)
	}
}

func (a *Agent) interfacesByInstance(networks []networkView) map[string]*workload.NetworkConfig {
	interfaces := map[string]*workload.NetworkConfig{}
	for _, n := range networks {
		network, err := netip.ParsePrefix(n.CIDR)
		if err != nil {
			a.log.Warn("skipping a network with an unusable cidr",
				"network", n.Name, "cidr", n.CIDR, "error", err)
			continue
		}

		slice, err := netip.ParsePrefix(n.Slice)
		if err != nil {
			a.log.Warn("skipping a network with an unusable slice",
				"network", n.Name, "slice", n.Slice, "error", err)
			continue
		}

		for _, nic := range n.NICs {
			interfaces[nic.InstanceID] = &workload.NetworkConfig{
				Bridge:       n.Bridge,
				BridgeAddr:   n.Gateway + "/" + strconv.Itoa(network.Bits()),
				IP:           nic.IP,
				Prefix:       slice.Bits(),
				Gateway:      n.Gateway,
				MAC:          nic.MAC,
				Nameserver:   n.Gateway,
				SearchDomain: n.Name + "." + dnsSuffix,
			}
		}
	}
	return interfaces
}

func (a *Agent) serveDNS(ctx context.Context, networks []networkView, records []dnsRecordView) {
	if a.resolver == nil {
		return
	}

	for _, n := range networks {
		if err := a.resolver.Listen(ctx, n.Gateway); err != nil {
			a.log.Warn("could not serve dns on the gateway",
				"network", n.Name, "gateway", n.Gateway, "error", err)
		}
	}

	zone := make(map[string]string, len(records))
	for _, record := range records {
		zone[record.FQDN] = record.IP
	}
	a.resolver.Update(zone)
}

func (a *Agent) applyRoutes(ctx context.Context, networks []networkView, nodes []nodeView) {
	if a.datapath == nil {
		return
	}

	var peers []peerView
	for _, n := range networks {
		peers = append(peers, n.Peers...)
	}
	if len(peers) == 0 {
		return
	}

	addresses := make(map[string]string, len(nodes))
	for _, n := range nodes {
		addresses[n.ID] = n.Address
	}

	routes := make([]workload.Route, 0, len(peers))
	for _, peer := range peers {
		via, known := addresses[peer.NodeID]
		if !known || via == "" {
			a.log.Warn("peer has no reachable address, skipping its route",
				"node", peer.NodeID, "slice", peer.Slice)
			continue
		}
		routes = append(routes, workload.Route{Slice: peer.Slice, Via: via})
	}
	if len(routes) == 0 {
		return
	}

	if err := a.datapath.ApplyRoutes(ctx, routes); err != nil {
		a.log.Warn("could not program peer routes", "error", err)
		return
	}
	a.log.Debug("peer routes programmed", "count", len(routes))
}

func (a *Agent) applyFilters(ctx context.Context, networks []networkView, isolations map[string]string) {
	if a.datapath == nil {
		return
	}

	filters := make([]workload.Filter, 0)
	for _, n := range networks {
		for _, nic := range n.NICs {
			filters = append(filters, workload.Filter{
				InstanceID: nic.InstanceID,
				Isolation:  isolations[nic.InstanceID],
				Bridge:     n.Bridge,
				IP:         nic.IP,
				MAC:        nic.MAC,
			})
		}
	}

	if err := a.datapath.ApplyFilters(ctx, filters); err != nil {
		a.log.Warn("could not apply anti-spoof rules", "error", err)
		return
	}
	a.log.Debug("anti-spoof rules applied", "interfaces", len(filters))
}

func (a *Agent) reconcileOne(
	ctx context.Context,
	in instanceView,
	iface *workload.NetworkConfig,
) (string, string) {
	runtime, known := a.runtimeFor(in.Isolation)
	if !known {
		return observedFailed, "this node has no runtime for isolation " + in.Isolation
	}

	spec := workload.Spec{
		InstanceID: in.ID,
		Name:       in.Name,
		Isolation:  in.Isolation,
		Image:      in.Image,
		Command:    in.Command,
		VCPU:       in.VCPU,
		MemoryMiB:  in.MemoryMiB,
		Network:    iface,
	}

	state, err := runtime.Status(ctx, in.ID)
	if err != nil {
		return observedFailed, "could not inspect the workload: " + err.Error()
	}

	switch in.DesiredState {
	case desiredRunning:
		if iface == nil {
			return observedPending, "waiting for an address"
		}
		return a.ensureRunning(ctx, runtime, spec, state, in.RestartPolicy)
	case desiredStopped:
		return a.ensureStopped(ctx, runtime, in.ID, state)
	default:
		return observedFailed, "unknown desired state " + in.DesiredState
	}
}

func (a *Agent) ensureRunning(
	ctx context.Context,
	runtime workload.Runtime,
	spec workload.Spec,
	state workload.State,
	policy string,
) (string, string) {
	switch state.Phase {
	case workload.PhaseRunning:
		return observedRunning, state.Message

	case workload.PhaseExited:
		return a.handleExit(ctx, runtime, spec, state, policy)

	default:
		return a.start(ctx, runtime, spec, "")
	}
}

func (a *Agent) handleExit(
	ctx context.Context,
	runtime workload.Runtime,
	spec workload.Spec,
	state workload.State,
	policy string,
) (string, string) {
	if a.takeHaltedByUser(spec.InstanceID) {
		return a.start(ctx, runtime, spec, "")
	}

	if !shouldRestart(policy, state.ExitCode) {
		if state.ExitCode == 0 {
			return observedStopped, state.Message
		}
		return observedFailed, state.Message
	}

	if allowed, wait := a.restartAllowed(spec.InstanceID); !allowed {
		return observedFailed, fmt.Sprintf("%s, restarting in %s (attempt %d)",
			state.Message, wait.Round(time.Second), a.restartAttempts(spec.InstanceID)+1)
	}

	attempt := a.noteRestart(spec.InstanceID)
	a.log.Info("restarting workload",
		"instance", spec.InstanceID,
		"exit_code", state.ExitCode,
		"attempt", attempt,
	)

	if err := runtime.Remove(ctx, spec.InstanceID); err != nil {
		a.log.Warn("could not clear the exited workload", "instance", spec.InstanceID, "error", err)
		return observedFailed, "could not clear the exited workload: " + err.Error()
	}

	return a.start(ctx, runtime, spec, fmt.Sprintf("restarted after %s", state.Message))
}

func (a *Agent) start(
	ctx context.Context,
	runtime workload.Runtime,
	spec workload.Spec,
	note string,
) (string, string) {
	if err := runtime.Start(ctx, spec); err != nil {
		a.log.Warn("could not start workload", "instance", spec.InstanceID, "error", err)
		return observedFailed, err.Error()
	}

	after, err := runtime.Status(ctx, spec.InstanceID)
	if err != nil {
		return observedFailed, "started but could not be inspected: " + err.Error()
	}
	if after.Phase != workload.PhaseRunning {
		return observedFailed, after.Message
	}

	a.noteStarted(spec.InstanceID)
	return observedRunning, note
}

func (a *Agent) ensureStopped(
	ctx context.Context,
	runtime workload.Runtime,
	instanceID string,
	state workload.State,
) (string, string) {
	a.noteHaltedByUser(instanceID)

	if state.Phase == workload.PhaseRunning {
		if err := runtime.Stop(ctx, instanceID); err != nil {
			a.log.Warn("could not stop workload", "instance", instanceID, "error", err)
			return observedFailed, err.Error()
		}
	}
	return observedStopped, ""
}
