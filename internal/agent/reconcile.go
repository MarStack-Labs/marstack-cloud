package agent

import (
	"context"
	"net/netip"
	"strconv"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	dnsSuffix = "internal"

	desiredRunning = "running"
	desiredStopped = "stopped"

	observedPending = "pending"
	observedRunning = "running"
	observedStopped = "stopped"
	observedFailed  = "failed"
)

func (a *Agent) reconcile(ctx context.Context) {
	if a.runtime == nil {
		return
	}

	assigned, err := a.client.assignedInstances(ctx, a.nodeID)
	if err != nil {
		a.log.Warn("could not read assigned instances", "error", err)
		return
	}

	networks, err := a.client.nodeNetworks(ctx, a.nodeID)
	if err != nil {
		a.log.Warn("could not read the node network view", "error", err)
		return
	}

	a.collectGarbage(ctx, assigned, networks)
	a.serveDNS(ctx, networks)

	interfaces := a.interfacesByInstance(networks)
	a.applyRoutes(ctx, networks)

	for _, in := range assigned {
		observed, message := a.reconcileOne(ctx, in, interfaces[in.ID])

		if in.ObservedState == observed && in.ObservedMessage == message {
			continue
		}
		if err := a.client.reportStatus(ctx, a.nodeID, in.ID, observed, message); err != nil {
			a.log.Warn("could not report status", "instance", in.ID, "error", err)
		}
	}
}

func (a *Agent) collectGarbage(ctx context.Context, assigned []instanceView, networks []networkView) {
	wanted := make(map[string]bool, len(assigned))
	instanceIDs := make([]string, 0, len(assigned))
	for _, in := range assigned {
		wanted[in.ID] = true
		instanceIDs = append(instanceIDs, in.ID)
	}

	if present, err := a.runtime.List(ctx); err != nil {
		a.log.Warn("could not list local workloads", "error", err)
	} else {
		for _, id := range present {
			if wanted[id] {
				continue
			}
			a.log.Info("removing a workload the control plane no longer knows", "instance", id)
			if err := a.runtime.Remove(ctx, id); err != nil {
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

func (a *Agent) serveDNS(ctx context.Context, networks []networkView) {
	if a.resolver == nil {
		return
	}

	for _, n := range networks {
		if err := a.resolver.Listen(ctx, n.Gateway); err != nil {
			a.log.Warn("could not serve dns on the gateway",
				"network", n.Name, "gateway", n.Gateway, "error", err)
		}
	}

	records, err := a.client.dnsRecords(ctx)
	if err != nil {
		a.log.Warn("could not read dns records", "error", err)
		return
	}

	zone := make(map[string]string, len(records))
	for _, record := range records {
		zone[record.FQDN] = record.IP
	}
	a.resolver.Update(zone)
}

func (a *Agent) applyRoutes(ctx context.Context, networks []networkView) {
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

	addresses, err := a.nodeAddresses(ctx)
	if err != nil {
		a.log.Warn("could not read node addresses", "error", err)
		return
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

func (a *Agent) nodeAddresses(ctx context.Context) (map[string]string, error) {
	nodes, err := a.client.nodes(ctx)
	if err != nil {
		return nil, err
	}

	addresses := make(map[string]string, len(nodes))
	for _, n := range nodes {
		addresses[n.ID] = n.Address
	}
	return addresses, nil
}

func (a *Agent) reconcileOne(
	ctx context.Context,
	in instanceView,
	iface *workload.NetworkConfig,
) (string, string) {
	spec := workload.Spec{
		InstanceID: in.ID,
		Name:       in.Name,
		Image:      in.Image,
		Command:    in.Command,
		VCPU:       in.VCPU,
		MemoryMiB:  in.MemoryMiB,
		Network:    iface,
	}

	state, err := a.runtime.Status(ctx, in.ID)
	if err != nil {
		return observedFailed, "could not inspect the workload: " + err.Error()
	}

	switch in.DesiredState {
	case desiredRunning:
		if iface == nil {
			return observedPending, "waiting for an address"
		}
		return a.ensureRunning(ctx, spec, state)
	case desiredStopped:
		return a.ensureStopped(ctx, in.ID, state)
	default:
		return observedFailed, "unknown desired state " + in.DesiredState
	}
}

func (a *Agent) ensureRunning(ctx context.Context, spec workload.Spec, state workload.State) (string, string) {
	switch state.Phase {
	case workload.PhaseRunning:
		return observedRunning, state.Message

	case workload.PhaseExited:
		return observedFailed, state.Message

	default:
		if err := a.runtime.Start(ctx, spec); err != nil {
			a.log.Warn("could not start workload", "instance", spec.InstanceID, "error", err)
			return observedFailed, err.Error()
		}

		after, err := a.runtime.Status(ctx, spec.InstanceID)
		if err != nil {
			return observedFailed, "started but could not be inspected: " + err.Error()
		}
		if after.Phase != workload.PhaseRunning {
			return observedFailed, after.Message
		}
		return observedRunning, ""
	}
}

func (a *Agent) ensureStopped(ctx context.Context, instanceID string, state workload.State) (string, string) {
	if state.Phase == workload.PhaseRunning {
		if err := a.runtime.Stop(ctx, instanceID); err != nil {
			a.log.Warn("could not stop workload", "instance", instanceID, "error", err)
			return observedFailed, err.Error()
		}
	}
	return observedStopped, ""
}
