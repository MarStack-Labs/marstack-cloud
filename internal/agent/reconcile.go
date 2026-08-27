package agent

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/catalog"
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

	a.refreshCatalog(ctx)

	state, err := a.readDesired(ctx)
	if err != nil {
		a.log.Warn("could not read the desired state", "error", err)

		if silence, cut := a.fenced(); cut {
			a.fence(ctx, silence)
		}
		return
	}

	a.noteContact()
	a.saveState(state)
	a.applyDesired(ctx, state, true)
}

func (a *Agent) readDesired(ctx context.Context) (cachedState, error) {
	nodeID := a.currentNodeID()

	assigned, err := a.client.assignedInstances(ctx, nodeID)
	if err != nil {
		return cachedState{}, fmt.Errorf("assigned instances: %w", err)
	}
	assigned = a.usableInstances(assigned)

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

	volumes, err := a.client.volumes(ctx, nodeID)
	if err != nil {
		return cachedState{}, fmt.Errorf("volumes: %w", err)
	}

	forwards, err := a.client.forwards(ctx, nodeID)
	if err != nil {
		return cachedState{}, fmt.Errorf("forwards: %w", err)
	}

	balancers, err := a.client.balancers(ctx, nodeID)
	if err != nil {
		return cachedState{}, fmt.Errorf("balancers: %w", err)
	}

	firewalls, err := a.client.firewalls(ctx, nodeID)
	if err != nil {
		return cachedState{}, fmt.Errorf("firewalls: %w", err)
	}

	return cachedState{
		Instances: assigned,
		Networks:  networks,
		Records:   records,
		Nodes:     nodes,
		Volumes:   volumes,
		Forwards:  forwards,
		Balancers: balancers,
		Firewalls: firewalls,
	}, nil
}

func (a *Agent) applyDesired(ctx context.Context, state cachedState, report bool) {
	if len(a.runtimes) == 0 {
		return
	}

	a.collectGarbage(ctx, state.Instances, state.Networks)
	a.tidyVolumes(state.Volumes)
	a.restoreVolumes(ctx, a.currentNodeID(), state.Volumes, state.Instances)
	a.applySnapshots(ctx, state.Volumes, state.Instances, report)

	if report {
		a.runBackups(ctx, a.currentNodeID(), state.Volumes, state.Instances)
	}
	a.tidyImages(ctx, state.Instances, report)

	if report {
		a.forgetMarks(state.Instances)
		a.reportUsage(ctx, state.Instances)
		a.runProbes(ctx, state.Balancers, state.Instances)
	}
	a.serveDNS(ctx, state.Networks, state.Records)

	interfaces := a.interfacesByInstance(state.Networks)
	disks := a.disksByInstance(ctx, state.Volumes)
	a.applyRoutes(ctx, state.Networks, state.Nodes)
	a.applyFilters(ctx, state.Networks, isolationsOf(state.Instances))
	a.applyForwards(ctx, state.Forwards, state.Balancers)
	a.applyGuards(ctx, state.Instances, state.Networks, state.Firewalls)

	for _, in := range state.Instances {
		a.plugDisks(ctx, in, disks[in.ID])

		observed, message := a.reconcileOne(ctx, in, interfaces[in.ID], disks[in.ID])
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

func (a *Agent) usableInstances(assigned []instanceView) []instanceView {
	usable := make([]instanceView, 0, len(assigned))
	for _, in := range assigned {
		if !safeInstanceID(in.ID) {
			a.log.Warn("refusing an instance with an unusable id", "instance", in.ID)
			continue
		}
		usable = append(usable, in)
	}
	return usable
}

func safeInstanceID(id string) bool {
	if !ids.HasPrefix(id, "i") || len(id) > 40 {
		return false
	}

	for _, char := range id[2:] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
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

func (a *Agent) applyGuards(
	ctx context.Context, assigned []instanceView, networks []networkView, firewalls []firewallView,
) {
	if a.datapath == nil {
		return
	}

	rules := make(map[string][]workload.GuardRule, len(firewalls))
	for _, f := range firewalls {
		set := make([]workload.GuardRule, 0, len(f.Rules))
		for _, rule := range f.Rules {
			set = append(set, workload.GuardRule{
				Protocol: rule.Protocol,
				FromPort: rule.FromPort,
				ToPort:   rule.ToPort,
				Source:   rule.Source,
			})
		}
		rules[f.ID] = set
	}

	addresses := map[string]string{}
	for _, network := range networks {
		for _, nic := range network.NICs {
			addresses[nic.InstanceID] = nic.IP
		}
	}

	guards := make([]workload.Guard, 0, len(assigned))
	for _, in := range assigned {
		if in.FirewallID == "" {
			continue
		}

		set, known := rules[in.FirewallID]
		if !known {
			a.log.Warn("an instance names a firewall this node cannot see",
				"instance", in.ID, "firewall", in.FirewallID)
			continue
		}

		guards = append(guards, workload.Guard{
			InstanceID: in.ID,
			Isolation:  in.Isolation,
			IP:         addresses[in.ID],
			Rules:      set,
		})
	}

	if err := a.datapath.ApplyGuards(ctx, guards); err != nil {
		a.log.Warn("could not apply the firewall rules", "error", err)
	}
}

func (a *Agent) applyForwards(ctx context.Context, forwards []forwardView, balancers []balancerView) {
	if a.datapath == nil {
		return
	}

	published := make([]workload.Publish, 0, len(forwards)+len(balancers))
	for _, f := range forwards {
		published = append(published, workload.Publish{
			Protocol:   f.Protocol,
			NodePort:   f.NodePort,
			TargetPort: f.TargetPort,
			Address:    f.Address,
		})
	}

	for _, b := range balancers {
		targets := make([]string, 0, len(b.Backends))
		for _, backend := range b.Backends {
			if !backend.Healthy || backend.Address == "" {
				continue
			}
			targets = append(targets, backend.Address)
		}
		if len(targets) == 0 {
			continue
		}

		published = append(published, workload.Publish{
			Protocol:   b.Protocol,
			NodePort:   b.ListenPort,
			TargetPort: b.TargetPort,
			Targets:    targets,
			Algorithm:  b.Algorithm,
		})
	}

	if err := a.datapath.ApplyForwards(ctx, published); err != nil {
		a.log.Warn("could not apply the published ports", "error", err)
	}
}

func (a *Agent) tidyVolumes(volumes []volumeView) {
	keep := make([]string, 0, len(volumes))
	for _, v := range volumes {
		keep = append(keep, v.ID)
	}

	for isolation, runtime := range a.runtimes {
		keeper, able := runtime.(workload.VolumeKeeper)
		if !able {
			continue
		}
		if err := keeper.PruneVolumes(keep); err != nil {
			a.log.Warn("could not tidy the volumes", "isolation", isolation, "error", err)
		}
	}
}

func (a *Agent) volumeIsBusy(ctx context.Context, v volumeView, assigned []instanceView) bool {
	if v.InstanceID == "" {
		return false
	}

	for _, in := range assigned {
		if in.ID != v.InstanceID {
			continue
		}

		runtime, known := a.runtimeFor(in.Isolation)
		if !known {
			return true
		}

		state, err := runtime.Status(ctx, in.ID)
		if err != nil {
			return true
		}
		return state.Phase == workload.PhaseRunning
	}
	return true
}

func (a *Agent) applySnapshots(
	ctx context.Context, volumes []volumeView, assigned []instanceView, report bool,
) {
	if len(volumes) == 0 {
		return
	}

	plans := make([]workload.SnapshotPlan, 0, len(volumes))
	grow := make([]workload.GrowPlan, 0, len(volumes))
	for _, v := range volumes {
		if a.volumeIsBusy(ctx, v, assigned) {
			continue
		}

		wanted := make([]string, 0, len(v.Snapshots))
		for _, snap := range v.Snapshots {
			wanted = append(wanted, snap.Name)
		}
		plan := workload.SnapshotPlan{
			VolumeID:  v.ID,
			Wanted:    wanted,
			RestoreTo: v.RestoreFrom,
		}
		if v.Encrypted {
			path, err := a.volumeKeyFile(ctx, v.ID)
			if err != nil {
				a.log.Warn("could not place the key of an encrypted volume, skipping snapshots",
					"volume", v.ID, "error", err)
				continue
			}
			plan.KeyFile = path
		}
		plans = append(plans, plan)
		grow = append(grow, workload.GrowPlan{
			VolumeID: v.ID,
			SizeGiB:  v.SizeGiB,
			KeyFile:  plan.KeyFile,
		})
	}

	if len(plans) == 0 {
		return
	}

	reports := make([]reportedVolumeBody, 0, len(plans))
	for _, runtime := range a.runtimes {
		keeper, able := runtime.(workload.VolumeKeeper)
		if !able {
			continue
		}

		for _, plan := range grow {
			if err := keeper.GrowVolume(plan); err != nil {
				a.log.Warn("could not grow a volume", "volume", plan.VolumeID, "error", err)
			}
		}

		for _, state := range keeper.SyncSnapshots(plans) {
			files := make([]reportedSnapshotBody, 0, len(state.Present))
			for _, file := range state.Present {
				files = append(files, reportedSnapshotBody{Name: file.Name, SizeBytes: file.Bytes})
			}
			reports = append(reports, reportedVolumeBody{
				VolumeID:  state.VolumeID,
				Snapshots: files,
				Restored:  state.Restored,
				Error:     state.Error,
			})
		}
	}

	if !report || len(reports) == 0 {
		return
	}
	if err := a.client.reportVolumes(ctx, a.currentNodeID(), reports); err != nil {
		a.log.Warn("could not report the volume snapshots", "error", err)
	}
}

func (a *Agent) tidyImages(ctx context.Context, assigned []instanceView, report bool) {
	if a.catalog == nil {
		return
	}

	inUse := make([]string, 0, len(assigned)*3)
	for _, in := range assigned {
		inUse = append(inUse, in.Image, in.ISO, in.Kernel)
	}

	if err := a.catalog.Prune(inUse); err != nil {
		a.log.Warn("could not tidy the staged images", "error", err)
	}

	if !report {
		return
	}

	staged, err := a.catalog.Staged()
	if err != nil {
		a.log.Warn("could not list the staged images", "error", err)
		return
	}

	files := make([]stagedImageBody, 0, len(staged))
	for _, file := range staged {
		files = append(files, stagedImageBody{ImageID: file.Origin, SizeBytes: file.Bytes})
	}

	if err := a.client.reportImages(ctx, a.currentNodeID(), files); err != nil {
		a.log.Warn("could not report the staged images", "error", err)
	}
}

func (a *Agent) refreshCatalog(ctx context.Context) {
	if a.catalog == nil {
		return
	}

	images, err := a.client.images(ctx, a.currentNodeID())
	if err != nil {
		a.log.Warn("could not read the image catalog, keeping the last one", "error", err)
		return
	}

	known := make([]catalog.Image, 0, len(images))
	for _, in := range images {
		known = append(known, catalog.Image{
			ID:       in.ID,
			Name:     in.Name,
			Kind:     in.Kind,
			Arch:     in.Arch,
			Source:   in.Source,
			Checksum: in.Checksum,
		})
	}
	a.catalog.Replace(known)
}

func (a *Agent) disksByInstance(
	ctx context.Context, volumes []volumeView,
) map[string][]workload.Disk {
	byInstance := map[string][]workload.Disk{}
	for _, v := range volumes {
		if v.InstanceID == "" {
			continue
		}

		disk := workload.Disk{ID: v.ID, Name: v.Name, SizeGiB: v.SizeGiB}
		if v.Encrypted {
			path, err := a.volumeKeyFile(ctx, v.ID)
			if err != nil {
				a.log.Warn("could not place the key of an encrypted volume, leaving the disk out",
					"volume", v.ID, "error", err)
				continue
			}
			disk.KeyFile = path
		}
		byInstance[v.InstanceID] = append(byInstance[v.InstanceID], disk)
	}
	return byInstance
}

func (a *Agent) reconcileOne(
	ctx context.Context,
	in instanceView,
	iface *workload.NetworkConfig,
	disks []workload.Disk,
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
		ISO:        in.ISO,
		Kernel:     in.Kernel,
		DiskGiB:    in.DiskGiB,
		Volumes:    disks,
		Command:    in.Command,
		VCPU:       in.VCPU,
		MemoryMiB:  in.MemoryMiB,
		SSHKeys:    in.SSHKeys,
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

	message := state.Message
	if failure := a.lastStartFailure(spec.InstanceID); failure != "" {
		message = failure
	}

	if !shouldRestart(policy, state.ExitCode) {
		if state.ExitCode == 0 {
			return observedStopped, message
		}
		return observedFailed, message
	}

	if allowed, wait := a.restartAllowed(spec.InstanceID); !allowed {
		return observedFailed, fmt.Sprintf("%s, restarting in %s (attempt %d)",
			message, wait.Round(time.Second), a.restartAttempts(spec.InstanceID)+1)
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

	return a.start(ctx, runtime, spec, fmt.Sprintf("restarted after %s", message))
}

func (a *Agent) start(
	ctx context.Context,
	runtime workload.Runtime,
	spec workload.Spec,
	note string,
) (string, string) {
	if silence, found := a.takeFenced(spec.InstanceID); found {
		note = "restarted after this node was fenced for " + silence.Round(time.Second).String()
	}
	if err := runtime.Start(ctx, spec); err != nil {
		a.log.Warn("could not start workload", "instance", spec.InstanceID, "error", err)
		a.noteStartFailure(spec.InstanceID, err.Error())
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
