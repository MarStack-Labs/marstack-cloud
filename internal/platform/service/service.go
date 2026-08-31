package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/events"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Registry interface {
	ExistsIn(ctx context.Context, id, projectID string) (bool, error)
}

type Workloads interface {
	Create(ctx context.Context, workload Workload) (string, error)
	Delete(ctx context.Context, projectID, instanceID string) error
	Alive(ctx context.Context, instanceIDs []string) (map[string]bool, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	workloads Workloads
	networks  Registry
	firewalls Registry
	events    events.Recorder
	sealing   *sealed.Keyring
	log       *slog.Logger
	now       clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, log: log, now: now}
}

func checkSelector(selector map[string]string) error {
	if len(selector) > MaxNodeSelector {
		return fault.Invalid("invalid_node_selector", fmt.Sprintf(
			"a node selector names at most %d labels, and %d were given",
			MaxNodeSelector, len(selector)))
	}
	for key, value := range selector {
		if err := validate.Name("node_selector_key", key); err != nil {
			return err
		}
		if err := validate.Name("node_selector_value", value); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) checkReferences(ctx context.Context, projectID string, t Template) error {
	named := []struct {
		registry Registry
		ids      []string
	}{
		{s.networks, append([]string{t.NetworkID}, t.ExtraNetworks...)},
		{s.firewalls, []string{t.FirewallID}},
	}

	for _, group := range named {
		if group.registry == nil {
			continue
		}
		for _, id := range group.ids {
			if id == "" {
				continue
			}
			known, err := group.registry.ExistsIn(ctx, id, projectID)
			if err != nil {
				return err
			}
			if !known {
				return fault.Invalid("unknown_reference", "nothing with id "+id+
					" exists in this project, and a template that names it would retire a "+
					"working replica to make one that cannot start")
			}
		}
	}
	return nil
}

func checkTemplate(t Template) error {
	if err := checkSelector(t.NodeSelector); err != nil {
		return err
	}
	if err := checkEnv(t.Env); err != nil {
		return err
	}
	if len(t.ExtraNetworks) > MaxExtraNetworks {
		return fault.Invalid("invalid_networks", fmt.Sprintf(
			"a replica takes at most %d networks beyond its first", MaxExtraNetworks))
	}
	if len(t.Files) > MaxFiles {
		return fault.Invalid("invalid_files", fmt.Sprintf(
			"a service carries at most %d files, and %d were given", MaxFiles, len(t.Files)))
	}
	if t.Image == "" && t.ISO == "" {
		return fault.Invalid("invalid_template",
			"a service needs an image or an iso to make replicas from")
	}
	return nil
}

func (s *service) sealTemplate(t Template) (Template, error) {
	envSealed, envKeyID, err := sealEnv(t.Env, s.sealing)
	if err != nil {
		return Template{}, err
	}

	filesSealed, err := sealFiles(t.Files, s.sealing)
	if err != nil {
		return Template{}, err
	}
	if filesSealed != "" && envKeyID == "" {
		envKeyID = s.sealing.ActiveID()
	}

	t.EnvSealed = envSealed
	t.EnvKeyID = envKeyID
	t.EnvNames = namesOf(t.Env)
	t.Env = nil
	t.FilesSealed = filesSealed
	t.FilePaths = pathsOf(t.Files)
	t.Files = nil
	return t, nil
}

func (s *service) create(ctx context.Context, params CreateParams) (Service, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Service{}, err
	}
	if len(params.Name) > MaxNameLength {
		return Service{}, fault.Invalid("invalid_name", fmt.Sprintf(
			"a service name must be at most %d characters, because every replica name is "+
				"built from it", MaxNameLength))
	}
	if err := checkReplicas(params.Replicas); err != nil {
		return Service{}, err
	}
	if err := checkTemplate(params.Template); err != nil {
		return Service{}, err
	}
	if err := s.checkReferences(ctx, params.ProjectID, params.Template); err != nil {
		return Service{}, err
	}

	template, err := s.sealTemplate(params.Template)
	if err != nil {
		return Service{}, err
	}

	at := s.now()
	svc := Service{
		ID:        ids.New("svc"),
		ProjectID: params.ProjectID,
		Name:      params.Name,
		Replicas:  params.Replicas,
		Revision:  1,
		Template:  template,
		CreatedAt: at,
		UpdatedAt: at,
	}

	if err := s.repo.insert(ctx, svc); err != nil {
		return Service{}, translate(err)
	}
	return svc, nil
}

func (s *service) update(ctx context.Context, params UpdateParams) (Service, error) {
	if err := checkTemplate(params.Template); err != nil {
		return Service{}, err
	}

	svc, err := s.find(ctx, params.ProjectID, params.ID)
	if err != nil {
		return Service{}, err
	}
	if err := s.checkReferences(ctx, params.ProjectID, params.Template); err != nil {
		return Service{}, err
	}

	template, err := s.sealTemplate(params.Template)
	if err != nil {
		return Service{}, err
	}

	if err := s.repo.setTemplate(ctx, svc.ID, template, svc.Revision+1, s.now()); err != nil {
		return Service{}, translate(err)
	}

	s.note(ctx, svc, events.Entry{
		Kind:    "service.revision_published",
		Subject: svc.ID,
		Message: svc.Name + " is now on revision " + strconv.Itoa(svc.Revision+1) +
			", and replicas will be replaced one at a time",
		Severity: events.Info,
	})
	return s.find(ctx, params.ProjectID, svc.ID)
}

func checkReplicas(replicas int) error {
	if replicas < 0 || replicas > MaxReplicas {
		return fault.Invalid("invalid_replicas", fmt.Sprintf(
			"replicas must be between 0 and %d", MaxReplicas))
	}
	return nil
}

func (s *service) scale(ctx context.Context, projectID, id string, replicas int) (Service, error) {
	if err := checkReplicas(replicas); err != nil {
		return Service{}, err
	}

	svc, err := s.find(ctx, projectID, id)
	if err != nil {
		return Service{}, err
	}
	if err := s.repo.setReplicas(ctx, svc.ID, replicas, s.now()); err != nil {
		return Service{}, translate(err)
	}
	return s.find(ctx, projectID, svc.ID)
}

func (s *service) find(ctx context.Context, projectID, id string) (Service, error) {
	svc, err := s.repo.byID(ctx, projectID, id)
	if errors.Is(err, errNotFound) {
		svc, err = s.repo.byName(ctx, projectID, id)
	}
	if err != nil {
		return Service{}, translate(err)
	}
	return svc, nil
}

func (s *service) membersOf(ctx context.Context, projectID, serviceID string) ([]string, error) {
	svc, err := s.find(ctx, projectID, serviceID)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(svc.Members))
	for _, member := range svc.Members {
		ids = append(ids, member.InstanceID)
	}
	return ids, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Service, error) {
	services, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return services, nil
}

func (s *service) remove(ctx context.Context, projectID, id string) error {
	svc, err := s.find(ctx, projectID, id)
	if err != nil {
		return err
	}

	for _, member := range svc.Members {
		if s.workloads == nil {
			break
		}
		if err := s.workloads.Delete(ctx, svc.ProjectID, member.InstanceID); err != nil {
			return fault.Internal(fmt.Errorf(
				"service %s could not delete replica %s, so nothing was removed: %w",
				svc.Name, member.InstanceID, err))
		}
	}

	if err := s.repo.delete(ctx, svc.ID); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) reconcile(ctx context.Context) (int, int, error) {
	if s.workloads == nil {
		return 0, 0, nil
	}

	services, err := s.repo.all(ctx)
	if err != nil {
		return 0, 0, translate(err)
	}

	var created, removed int
	for _, svc := range services {
		up, down, err := s.reconcileOne(ctx, svc)
		if err != nil {
			s.log.Warn("could not reconcile a service", "service", svc.Name, "error", err)
			continue
		}
		created += up
		removed += down
	}
	return created, removed, nil
}

func (s *service) reconcileOne(ctx context.Context, svc Service) (int, int, error) {
	present, err := s.livingMembers(ctx, svc)
	if err != nil {
		return 0, 0, err
	}

	if len(present) > svc.Replicas {
		return 0, s.shrinkTo(ctx, svc, present), nil
	}

	var retired int
	if len(present) == svc.Replicas {
		present, retired = s.retireStale(ctx, svc, present)
	}

	if len(present) < svc.Replicas {
		created, _, err := s.growTo(ctx, svc, present)
		return created, retired, err
	}

	s.clearBlocked(ctx, svc)
	return 0, retired, nil
}

func partition(present []Member, revision int, staleFirst bool) []Member {
	stale := make([]Member, 0, len(present))
	fresh := make([]Member, 0, len(present))
	for _, member := range present {
		if member.Revision == revision {
			fresh = append(fresh, member)
			continue
		}
		stale = append(stale, member)
	}
	if len(stale) == 0 {
		return present
	}
	if staleFirst {
		return append(stale, fresh...)
	}
	return append(fresh, stale...)
}

func (s *service) retireStale(ctx context.Context, svc Service, present []Member) ([]Member, int) {
	ordered := partition(present, svc.Revision, true)

	retired := 0
	for _, member := range ordered {
		if retired == MaxReplacePerPass {
			break
		}
		if member.Revision == svc.Revision {
			break
		}

		if err := s.workloads.Delete(ctx, svc.ProjectID, member.InstanceID); err != nil {
			s.log.Warn("could not retire a replica of an older revision",
				"service", svc.Name, "instance", member.InstanceID, "error", err)
			break
		}
		if err := s.repo.removeMember(ctx, svc.ID, member.InstanceID); err != nil {
			s.log.Warn("retired a replica but kept it in the service",
				"service", svc.Name, "instance", member.InstanceID, "error", err)
			break
		}
		retired++

		s.note(ctx, svc, events.Entry{
			Kind:    "service.replica_retired",
			Subject: member.InstanceID,
			Message: svc.Name + " retired a replica on revision " +
				strconv.Itoa(member.Revision) + " to make room for revision " +
				strconv.Itoa(svc.Revision),
			Severity: events.Info,
		})
	}

	if retired == 0 {
		return present, 0
	}
	return ordered[retired:], retired
}

func (s *service) livingMembers(ctx context.Context, svc Service) ([]Member, error) {
	if len(svc.Members) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(svc.Members))
	for _, member := range svc.Members {
		ids = append(ids, member.InstanceID)
	}

	alive, err := s.workloads.Alive(ctx, ids)
	if err != nil {
		return nil, err
	}

	present := make([]Member, 0, len(svc.Members))
	for _, member := range svc.Members {
		if alive[member.InstanceID] {
			present = append(present, member)
			continue
		}
		if err := s.repo.removeMember(ctx, svc.ID, member.InstanceID); err != nil {
			return nil, err
		}
		s.note(ctx, svc, events.Entry{
			Kind:     "service.replica_lost",
			Subject:  member.InstanceID,
			Message:  "replica of " + svc.Name + " no longer exists, making a replacement",
			Severity: events.Warn,
		})
	}
	return present, nil
}

func (s *service) growTo(ctx context.Context, svc Service, present []Member) (int, int, error) {
	wanted := svc.Replicas - len(present)
	if wanted > MaxCreatePerPass {
		wanted = MaxCreatePerPass
	}

	env, err := openEnv(svc.Template.EnvSealed, svc.Template.EnvKeyID, s.sealing)
	if err != nil {
		s.block(ctx, svc, err.Error())
		return 0, 0, nil
	}

	files, err := openFiles(svc.Template.FilesSealed, svc.Template.EnvKeyID, s.sealing)
	if err != nil {
		s.block(ctx, svc, err.Error())
		return 0, 0, nil
	}

	created := 0
	for range wanted {
		id, err := s.workloads.Create(ctx, Workload{
			ProjectID: svc.ProjectID,
			Name:      replicaName(svc.Name),
			Template:  svc.Template,
			Env:       env,
			Files:     files,
		})
		if err != nil {
			s.block(ctx, svc, err.Error())
			return created, 0, nil
		}

		if err := s.repo.addMember(ctx, svc.ID, id, svc.Revision, s.now()); err != nil {
			return created, 0, err
		}
		created++

		s.note(ctx, svc, events.Entry{
			Kind:    "service.replica_added",
			Subject: id,
			Message: svc.Name + " grew to " + strconv.Itoa(len(present)+created) +
				" of " + strconv.Itoa(svc.Replicas),
			Severity: events.Info,
		})
	}

	s.clearBlocked(ctx, svc)
	return created, 0, nil
}

func (s *service) shrinkTo(ctx context.Context, svc Service, present []Member) int {
	ordered := partition(present, svc.Revision, false)

	removed := 0
	for i := len(ordered) - 1; i >= svc.Replicas; i-- {
		member := ordered[i]

		if err := s.workloads.Delete(ctx, svc.ProjectID, member.InstanceID); err != nil {
			s.log.Warn("could not remove a replica",
				"service", svc.Name, "instance", member.InstanceID, "error", err)
			continue
		}
		if err := s.repo.removeMember(ctx, svc.ID, member.InstanceID); err != nil {
			s.log.Warn("removed a replica but kept it in the service",
				"service", svc.Name, "instance", member.InstanceID, "error", err)
			continue
		}
		removed++

		s.note(ctx, svc, events.Entry{
			Kind:    "service.replica_removed",
			Subject: member.InstanceID,
			Message: svc.Name + " shrank to " + strconv.Itoa(len(present)-removed) +
				" of " + strconv.Itoa(svc.Replicas),
			Severity: events.Info,
		})
	}

	s.clearBlocked(ctx, svc)
	return removed
}

func (s *service) block(ctx context.Context, svc Service, reason string) {
	reason = clip(reason, MaxNameLength*8)
	if svc.Blocked == reason {
		return
	}

	if err := s.repo.setBlocked(ctx, svc.ID, reason, s.now()); err != nil {
		s.log.Warn("could not record why a service is stuck", "service", svc.Name, "error", err)
	}
	s.note(ctx, svc, events.Entry{
		Kind:     "service.blocked",
		Subject:  svc.ID,
		Message:  svc.Name + " cannot reach " + strconv.Itoa(svc.Replicas) + " replicas: " + reason,
		Severity: events.Error,
	})
}

func (s *service) clearBlocked(ctx context.Context, svc Service) {
	if svc.Blocked == "" {
		return
	}
	if err := s.repo.setBlocked(ctx, svc.ID, "", s.now()); err != nil {
		s.log.Warn("could not clear a service block", "service", svc.Name, "error", err)
	}
}

func (s *service) note(ctx context.Context, svc Service, entry events.Entry) {
	if s.events == nil {
		return
	}
	entry.ProjectID = svc.ProjectID
	s.events.Record(ctx, entry)
}

func replicaName(name string) string {
	suffix := ids.New("r")
	if len(suffix) > SuffixLength+2 {
		suffix = suffix[len(suffix)-SuffixLength:]
	}
	return name + "-" + suffix
}

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("service_not_found", "no service with that name or id exists")
	case errors.Is(err, errNameUsed):
		return fault.Conflict("service_name_taken",
			"a service with that name already exists in this project")
	default:
		return err
	}
}
