package app

import (
	"context"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/platform/autoscale"
	"github.com/marstack-labs/marstack-cloud/internal/platform/backup"
	"github.com/marstack-labs/marstack-cloud/internal/platform/balancer"
	"github.com/marstack-labs/marstack-cloud/internal/platform/dns"
	"github.com/marstack-labs/marstack-cloud/internal/platform/event"
	"github.com/marstack-labs/marstack-cloud/internal/platform/forward"
	"github.com/marstack-labs/marstack-cloud/internal/platform/instance"
	"github.com/marstack-labs/marstack-cloud/internal/platform/job"
	"github.com/marstack-labs/marstack-cloud/internal/platform/network"
	"github.com/marstack-labs/marstack-cloud/internal/platform/node"
	"github.com/marstack-labs/marstack-cloud/internal/platform/quota"
	"github.com/marstack-labs/marstack-cloud/internal/platform/scheduler"
	"github.com/marstack-labs/marstack-cloud/internal/platform/service"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
	"github.com/marstack-labs/marstack-cloud/internal/platform/usage"
	"github.com/marstack-labs/marstack-cloud/internal/platform/volume"
	"github.com/marstack-labs/marstack-cloud/internal/platform/webhook"
)

type nodeSource struct {
	nodes *node.Module
}

func (s nodeSource) UnreachableNodes(ctx context.Context, grace time.Duration) ([]string, error) {
	lost, err := s.nodes.NodesUnreachableFor(ctx, grace)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(lost))
	for _, n := range lost {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

func (s nodeSource) DrainingNodes(ctx context.Context) ([]string, error) {
	draining, err := s.nodes.DrainingNodes(ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(draining))
	for _, n := range draining {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

func (s nodeSource) FinishDraining(ctx context.Context, nodeID string) error {
	return s.nodes.FinishDraining(ctx, nodeID)
}

func (s nodeSource) ReadyNodes(ctx context.Context) ([]scheduler.Candidate, error) {
	ready, err := s.nodes.ReadyNodes(ctx)
	if err != nil {
		return nil, err
	}

	candidates := make([]scheduler.Candidate, 0, len(ready))
	for _, n := range ready {
		candidates = append(candidates, scheduler.Candidate{
			ID: n.ID, Name: n.Name, Zone: n.Zone, Labels: n.Labels,
		})
	}
	return candidates, nil
}

type instanceSource struct {
	instances *instance.Module
}

func (s instanceSource) PendingPlacement(ctx context.Context) ([]scheduler.Pending, error) {
	waiting, err := s.instances.PendingPlacement(ctx)
	if err != nil {
		return nil, err
	}

	pending := make([]scheduler.Pending, 0, len(waiting))
	for _, in := range waiting {
		pending = append(pending, scheduler.Pending{
			ID:        in.ID,
			ProjectID: in.ProjectID,
			Name:      in.Name,
			NetworkID: in.NetworkID,
			Group:     in.Group,
			Strict:    in.Strict,
			Selector:  in.NodeSelector,
			Extra:     in.ExtraNetworks,
		})
	}
	return pending, nil
}

func (s instanceSource) StrandedOn(ctx context.Context, nodeIDs []string) ([]scheduler.Stranded, error) {
	stranded, err := s.instances.StrandedOn(ctx, nodeIDs)
	if err != nil {
		return nil, err
	}

	out := make([]scheduler.Stranded, 0, len(stranded))
	for _, in := range stranded {
		out = append(out, scheduler.Stranded{
			ID:        in.ID,
			ProjectID: in.ProjectID,
			Name:      in.Name,
			NodeID:    in.NodeID,
			Isolation: string(in.Isolation),
		})
	}
	return out, nil
}

func (s instanceSource) ReleasePlacement(ctx context.Context, instanceID, nodeID string) error {
	return s.instances.ReleasePlacement(ctx, instanceID, nodeID)
}

func (s instanceSource) AssignedCounts(ctx context.Context) (map[string]int, error) {
	return s.instances.AssignedCounts(ctx)
}

func (s instanceSource) Assign(ctx context.Context, instanceID, nodeID string) error {
	return s.instances.Assign(ctx, instanceID, nodeID)
}

type addressSource struct {
	networks *network.Module
}

func (s addressSource) Allocate(ctx context.Context, instanceID, networkID, nodeID string) error {
	return s.networks.Allocate(ctx, instanceID, networkID, nodeID)
}

type volumeInstances struct {
	instances *instance.Module
}

func (s volumeInstances) Placement(ctx context.Context, instanceID string) (volume.Placement, error) {
	in, err := s.instances.Get(ctx, instanceID)
	if err != nil {
		return volume.Placement{}, err
	}
	return volume.Placement{
		ProjectID: in.ProjectID,
		NodeID:    in.NodeID,
		Isolation: string(in.Isolation),
		Running:   in.Desired == instance.DesiredRunning,
	}, nil
}

type forwardAddresses struct {
	networks  *network.Module
	instances *instance.Module
}

func (s forwardAddresses) Endpoint(ctx context.Context, instanceID string) (forward.Endpoint, error) {
	in, err := s.instances.Get(ctx, instanceID)
	if err != nil {
		return forward.Endpoint{}, err
	}

	nic, err := s.networks.NICOf(ctx, instanceID)
	if err != nil {
		return forward.Endpoint{ProjectID: in.ProjectID}, nil
	}
	return forward.Endpoint{
		ProjectID: in.ProjectID,
		NodeID:    nic.NodeID,
		Address:   nic.IP,
		Address6:  nic.IP6,
	}, nil
}

type balancerMembers struct {
	networks  *network.Module
	instances *instance.Module
}

func (s balancerMembers) Member(ctx context.Context, instanceID string) (balancer.Member, error) {
	in, err := s.instances.Get(ctx, instanceID)
	if err != nil {
		return balancer.Member{}, err
	}

	nic, err := s.networks.NICOf(ctx, instanceID)
	if err != nil {
		return balancer.Member{ProjectID: in.ProjectID, NodeID: in.NodeID}, nil
	}
	return balancer.Member{
		ProjectID: in.ProjectID,
		NodeID:    in.NodeID,
		Address:   nic.IP,
		Address6:  nic.IP6,
		Running:   in.Observed == instance.ObservedRunning,
	}, nil
}

type webhookEvents struct {
	events *event.Module
}

func (w webhookEvents) Since(
	ctx context.Context, afterID int64, limit int,
) ([]webhook.Event, error) {
	entries, err := w.events.Since(ctx, afterID, limit)
	if err != nil {
		return nil, err
	}

	out := make([]webhook.Event, 0, len(entries))
	for _, entry := range entries {
		out = append(out, webhook.Event{
			ID:        entry.ID,
			At:        entry.At,
			ProjectID: entry.ProjectID,
			Kind:      entry.Kind,
			Subject:   entry.Subject,
			NodeID:    entry.NodeID,
			Message:   entry.Message,
			Severity:  entry.Severity,
		})
	}
	return out, nil
}

func (w webhookEvents) NewestID(ctx context.Context) (int64, error) {
	return w.events.NewestID(ctx)
}

type scalableServices struct {
	services *service.Module
}

func asGroup(m *service.Module, s service.Service) autoscale.Group {
	members := make([]autoscale.Member, 0, len(s.Members))
	for _, member := range s.Members {
		members = append(members, autoscale.Member{
			InstanceID: member.InstanceID,
			CreatedAt:  member.CreatedAt,
		})
	}
	return autoscale.Group{
		ServiceID: s.ID,
		ProjectID: s.ProjectID,
		Name:      s.Name,
		Replicas:  s.Replicas,
		Settled:   m.Settled(s),
		Members:   members,
	}
}

func (s scalableServices) GroupOf(ctx context.Context, projectID,
	serviceID string) (autoscale.Group, error) {
	found, err := s.services.Describe(ctx, projectID, serviceID)
	if err != nil {
		return autoscale.Group{}, err
	}
	return asGroup(s.services, found), nil
}

func (s scalableServices) Groups(ctx context.Context) ([]autoscale.Group, error) {
	all, err := s.services.All(ctx)
	if err != nil {
		return nil, err
	}

	groups := make([]autoscale.Group, 0, len(all))
	for _, one := range all {
		groups = append(groups, asGroup(s.services, one))
	}
	return groups, nil
}

func (s scalableServices) Scale(ctx context.Context, projectID, serviceID string,
	replicas int) error {
	return s.services.Scale(ctx, projectID, serviceID, replicas)
}

type instanceLoad struct {
	usage *usage.Module
}

func (l instanceLoad) SamplesOf(ctx context.Context) (map[string]autoscale.Sample, error) {
	samples, err := l.usage.Instances(ctx)
	if err != nil {
		return nil, err
	}

	held := make(map[string]autoscale.Sample, len(samples))
	for _, sample := range samples {
		held[sample.InstanceID] = autoscale.Sample{
			CPUPercent: sample.CPUPercent,
			ReportedAt: sample.ReportedAt,
		}
	}
	return held, nil
}

type jobWorkloads struct {
	instances *instance.Module
}

func (w jobWorkloads) Create(ctx context.Context, workload job.Workload) (string, error) {
	created, err := w.instances.Create(ctx, instance.CreateParams{
		ProjectID:     workload.ProjectID,
		Name:          workload.Name,
		Isolation:     workload.Template.Isolation,
		Image:         workload.Template.Image,
		Kernel:        workload.Template.Kernel,
		Command:       workload.Template.Command,
		NetworkID:     workload.Template.NetworkID,
		FirewallID:    workload.Template.FirewallID,
		VCPU:          workload.Template.VCPU,
		MemoryMiB:     workload.Template.MemoryMiB,
		NodeSelector:  workload.Template.NodeSelector,
		Env:           workload.Env,
		Files:         filesOf(workload.Files),
		RestartPolicy: string(instance.RestartNever),
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func filesOf(files []job.File) []instance.File {
	dropped := make([]instance.File, 0, len(files))
	for _, f := range files {
		dropped = append(dropped, instance.File{Path: f.Path, Content: f.Content, Mode: f.Mode})
	}
	return dropped
}

func (w jobWorkloads) Delete(ctx context.Context, projectID, instanceID string) error {
	return w.instances.Delete(ctx, instanceID, projectID)
}

func (w jobWorkloads) StateOf(ctx context.Context, instanceID string) (string, *int, string, error) {
	found, err := w.instances.Get(ctx, instanceID)
	if err != nil {
		return "", nil, "", err
	}
	return string(found.Observed), found.ExitCode, found.ObservedMessage, nil
}

type logInstances struct {
	instances *instance.Module
}

func (l logInstances) NodeOf(ctx context.Context, instanceID string) (string, string, error) {
	found, err := l.instances.Get(ctx, instanceID)
	if err != nil {
		return "", "", err
	}
	return found.NodeID, found.ProjectID, nil
}

type usageWorkloads struct {
	instances *instance.Module
}

func (w usageWorkloads) IDsIn(ctx context.Context, projectID string) ([]string, error) {
	held, err := w.instances.ListIn(ctx, projectID)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(held))
	for _, in := range held {
		ids = append(ids, in.ID)
	}
	return ids, nil
}

func instanceFiles(files []service.File) []instance.File {
	out := make([]instance.File, 0, len(files))
	for _, f := range files {
		out = append(out, instance.File{Path: f.Path, Content: f.Content, Mode: f.Mode})
	}
	return out
}

type serviceWorkloads struct {
	instances *instance.Module
}

func (w serviceWorkloads) Create(ctx context.Context, workload service.Workload) (string, error) {
	created, err := w.instances.Create(ctx, instance.CreateParams{
		ProjectID:     workload.ProjectID,
		Name:          workload.Name,
		Group:         workload.Template.Group,
		Strict:        workload.Template.Strict,
		Keys:          workload.Template.Keys,
		Isolation:     workload.Template.Isolation,
		Image:         workload.Template.Image,
		ISO:           workload.Template.ISO,
		Kernel:        workload.Template.Kernel,
		DiskGiB:       workload.Template.DiskGiB,
		FirewallID:    workload.Template.FirewallID,
		Command:       workload.Template.Command,
		NetworkID:     workload.Template.NetworkID,
		RestartPolicy: workload.Template.RestartPolicy,
		VCPU:          workload.Template.VCPU,
		MemoryMiB:     workload.Template.MemoryMiB,
		NodeSelector:  workload.Template.NodeSelector,
		ExtraNetworks: workload.Template.ExtraNetworks,
		Env:           workload.Env,
		Files:         instanceFiles(workload.Files),
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (w serviceWorkloads) Delete(ctx context.Context, projectID, instanceID string) error {
	return w.instances.Delete(ctx, instanceID, projectID)
}

func (w serviceWorkloads) StatesOf(
	ctx context.Context, instanceIDs []string,
) (map[string]string, error) {
	states := make(map[string]string, len(instanceIDs))
	for _, id := range instanceIDs {
		found, err := w.instances.Get(ctx, id)
		if err != nil {
			continue
		}
		states[id] = string(found.Observed)
	}
	return states, nil
}

const staleLoad = 30 * time.Second

type nodeLoad struct {
	usage *usage.Module
	now   func() time.Time
}

func (s nodeLoad) NodeLoad(ctx context.Context) (map[string]scheduler.Load, error) {
	samples, err := s.usage.Nodes(ctx)
	if err != nil {
		return nil, err
	}

	load := make(map[string]scheduler.Load, len(samples))
	for _, sample := range samples {
		load[sample.NodeID] = scheduler.Load{
			MemoryFreeMiB: sample.MemoryMiB - sample.MemoryUsedMiB,
			Fresh:         s.now().Sub(sample.ReportedAt) < staleLoad,
		}
	}
	return load, nil
}

type dnsInstances struct {
	instances *instance.Module
}

func (s dnsInstances) AllInstances(ctx context.Context) ([]dns.InstanceRef, error) {
	all, err := s.instances.All(ctx)
	if err != nil {
		return nil, err
	}

	refs := make([]dns.InstanceRef, 0, len(all))
	for _, in := range all {
		refs = append(refs, dns.InstanceRef{ID: in.ID, Name: in.Name, NetworkID: in.NetworkID})
	}
	return refs, nil
}

type projectOccupancy struct {
	tokens    *token.Module
	instances *instance.Module
	volumes   *volume.Module
}

func (o projectOccupancy) ResourcesIn(ctx context.Context, projectID string) (int, error) {
	held, err := o.tokens.CountIn(ctx, projectID)
	if err != nil {
		return 0, err
	}

	running, err := o.instances.FootprintIn(ctx, projectID)
	if err != nil {
		return 0, err
	}

	stored, err := o.volumes.FootprintIn(ctx, projectID)
	if err != nil {
		return 0, err
	}
	return held + running.Instances + stored.Volumes, nil
}

type projectUsage struct {
	instances *instance.Module
	volumes   *volume.Module
}

func (u projectUsage) InProject(ctx context.Context, projectID string) (quota.Consumed, error) {
	running, err := u.instances.FootprintIn(ctx, projectID)
	if err != nil {
		return quota.Consumed{}, err
	}

	stored, err := u.volumes.FootprintIn(ctx, projectID)
	if err != nil {
		return quota.Consumed{}, err
	}

	return quota.Consumed{
		Instances: running.Instances,
		VCPU:      running.VCPU,
		MemoryMiB: running.MemoryMiB,
		Volumes:   stored.Volumes,
		VolumeGiB: stored.SizeGiB,
	}, nil
}

type instanceQuota struct {
	quotas *quota.Module
}

func (q instanceQuota) AdmitGrowth(
	ctx context.Context, projectID string, vcpu, memoryMiB int,
) error {
	return q.quotas.Admit(ctx, projectID, quota.Claim{
		VCPU:      vcpu,
		MemoryMiB: memoryMiB,
	})
}

func (q instanceQuota) AdmitInstance(
	ctx context.Context, projectID string, vcpu, memoryMiB int,
) error {
	return q.quotas.Admit(ctx, projectID, quota.Claim{
		Instances: 1,
		VCPU:      vcpu,
		MemoryMiB: memoryMiB,
	})
}

type volumeQuota struct {
	quotas *quota.Module
}

func (q volumeQuota) AdmitVolume(ctx context.Context, projectID string, sizeGiB int) error {
	return q.quotas.Admit(ctx, projectID, quota.Claim{Volumes: 1, VolumeGiB: sizeGiB})
}

func (q volumeQuota) AdmitVolumeGrowth(
	ctx context.Context, projectID string, extraGiB int,
) error {
	return q.quotas.Admit(ctx, projectID, quota.Claim{VolumeGiB: extraGiB})
}

type backupVolumes struct {
	volumes *volume.Module
}

func (s backupVolumes) Source(
	ctx context.Context, volumeID, projectID string,
) (backup.Source, error) {
	v, err := s.volumes.Find(ctx, volumeID, projectID)
	if err != nil {
		return backup.Source{}, err
	}
	return backup.Source{
		ID:        v.ID,
		ProjectID: v.ProjectID,
		NodeID:    v.NodeID,
		Name:      v.Name,
		Encrypted: v.Encrypted,
		KeySealed: v.KeySealed,
		KeyID:     v.KeyID,
	}, nil
}

type volumeBackups struct {
	backups *backup.Module
}

func (s volumeBackups) Restorable(
	ctx context.Context, id, projectID string,
) (volume.Restorable, error) {
	envelope, err := s.backups.Restorable(ctx, id, projectID)
	if err != nil {
		return volume.Restorable{}, err
	}
	return volume.Restorable{
		SizeBytes: envelope.SizeBytes,
		KeySealed: envelope.KeySealed,
		KeyID:     envelope.KeyID,
	}, nil
}

func (s instanceSource) GroupCounts(ctx context.Context, group string) (map[string]int, error) {
	return s.instances.GroupCounts(ctx, group)
}

func (s instanceSource) HoldPlacement(ctx context.Context, instanceID, reason string) error {
	return s.instances.HoldPlacement(ctx, instanceID, reason)
}
