package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var (
	errNotFound = errors.New("service not found")
	errNameUsed = errors.New("service name already used")
)

const columns = `id, project_id, name, replicas, revision, isolation, image, iso, kernel, disk_gib,
	firewall_id, command, network_id, restart_policy, vcpu, memory_mib,
	placement_group, placement_strict, node_selector, extra_networks,
	env, env_key_id, env_names, files, file_paths, ssh_keys, reaped, blocked,
	created_at, updated_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, s Service) error {
	command, err := json.Marshal(s.Template.Command)
	if err != nil {
		return fmt.Errorf("encode the command: %w", err)
	}
	keys, err := json.Marshal(s.Template.Keys)
	if err != nil {
		return fmt.Errorf("encode the keys: %w", err)
	}

	selector, err := json.Marshal(s.Template.NodeSelector)
	if err != nil {
		return fmt.Errorf("encode the node selector: %w", err)
	}

	extra, err := json.Marshal(s.Template.ExtraNetworks)
	if err != nil {
		return fmt.Errorf("encode the extra networks: %w", err)
	}

	envNames, err := json.Marshal(s.Template.EnvNames)
	if err != nil {
		return fmt.Errorf("encode the environment names: %w", err)
	}

	filePaths, err := json.Marshal(s.Template.FilePaths)
	if err != nil {
		return fmt.Errorf("encode the file paths: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO services (`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, s.Name, s.Replicas, s.Revision, s.Template.Isolation, s.Template.Image,
		s.Template.ISO, s.Template.Kernel, s.Template.DiskGiB, s.Template.FirewallID,
		string(command), s.Template.NetworkID, s.Template.RestartPolicy, s.Template.VCPU,
		s.Template.MemoryMiB, s.Template.Group, s.Template.Strict, string(selector),
		string(extra), s.Template.EnvSealed, s.Template.EnvKeyID, string(envNames),
		s.Template.FilesSealed, string(filePaths), string(keys), s.Reaped, s.Blocked,
		s.CreatedAt.Format(time.RFC3339Nano), s.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameUsed
		}
		return fmt.Errorf("insert service: %w", err)
	}
	return nil
}

func (r *repository) setReplicas(ctx context.Context, id string, replicas int, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE services SET replicas = ?, updated_at = ? WHERE id = ?`,
		replicas, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set replicas: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set replicas: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) setTemplate(ctx context.Context, id string, t Template,
	revision int, at time.Time) error {
	command, err := json.Marshal(t.Command)
	if err != nil {
		return fmt.Errorf("encode the command: %w", err)
	}
	keys, err := json.Marshal(t.Keys)
	if err != nil {
		return fmt.Errorf("encode the keys: %w", err)
	}
	selector, err := json.Marshal(t.NodeSelector)
	if err != nil {
		return fmt.Errorf("encode the node selector: %w", err)
	}
	extra, err := json.Marshal(t.ExtraNetworks)
	if err != nil {
		return fmt.Errorf("encode the extra networks: %w", err)
	}
	envNames, err := json.Marshal(t.EnvNames)
	if err != nil {
		return fmt.Errorf("encode the environment names: %w", err)
	}
	filePaths, err := json.Marshal(t.FilePaths)
	if err != nil {
		return fmt.Errorf("encode the file paths: %w", err)
	}

	result, err := r.db.ExecContext(ctx,
		`UPDATE services SET revision = ?, isolation = ?, image = ?, iso = ?, kernel = ?,
			disk_gib = ?, firewall_id = ?, command = ?, network_id = ?, restart_policy = ?,
			vcpu = ?, memory_mib = ?, placement_group = ?, placement_strict = ?,
			node_selector = ?, extra_networks = ?, env = ?, env_key_id = ?, env_names = ?,
			files = ?, file_paths = ?, ssh_keys = ?, updated_at = ?
			WHERE id = ?`,
		revision, t.Isolation, t.Image, t.ISO, t.Kernel, t.DiskGiB, t.FirewallID,
		string(command), t.NetworkID, t.RestartPolicy, t.VCPU, t.MemoryMiB, t.Group, t.Strict,
		string(selector), string(extra), t.EnvSealed, t.EnvKeyID, string(envNames),
		t.FilesSealed, string(filePaths), string(keys), at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set the template: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set the template: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) setReaped(ctx context.Context, id string, reaped int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE services SET reaped = ? WHERE id = ?`, reaped, id)
	if err != nil {
		return fmt.Errorf("set the replacement count: %w", err)
	}
	return nil
}

func (r *repository) setBlocked(ctx context.Context, id, reason string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE services SET blocked = ?, updated_at = ? WHERE id = ?`,
		reason, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set the blocked reason: %w", err)
	}
	return nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Service, error) {
	return r.load(ctx,
		`SELECT `+columns+` FROM services WHERE project_id = ? ORDER BY name`, projectID)
}

func (r *repository) all(ctx context.Context) ([]Service, error) {
	return r.load(ctx, `SELECT `+columns+` FROM services ORDER BY id`)
}

func (r *repository) byID(ctx context.Context, projectID, id string) (Service, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM services WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return Service{}, err
	}
	if len(found) == 0 {
		return Service{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) byName(ctx context.Context, projectID, name string) (Service, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM services WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return Service{}, err
	}
	if len(found) == 0 {
		return Service{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) load(ctx context.Context, query string, args ...any) ([]Service, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()

	services := make([]Service, 0, 8)
	for rows.Next() {
		s, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		services = append(services, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}

	for i := range services {
		members, err := r.membersOf(ctx, services[i].ID)
		if err != nil {
			return nil, err
		}
		services[i].Members = members
	}
	return services, nil
}

func (r *repository) membersOf(ctx context.Context, serviceID string) ([]Member, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT instance_id, revision, created_at FROM service_members
			WHERE service_id = ? ORDER BY created_at, instance_id`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	members := make([]Member, 0, 4)
	for rows.Next() {
		var (
			member  Member
			created string
		)
		if err := rows.Scan(&member.InstanceID, &member.Revision, &created); err != nil {
			return nil, fmt.Errorf("scan a member: %w", err)
		}

		at, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		member.CreatedAt = at
		members = append(members, member)
	}
	return members, rows.Err()
}

func (r *repository) addMember(ctx context.Context, serviceID, instanceID string,
	revision int, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO service_members (service_id, instance_id, revision, created_at)
			VALUES (?, ?, ?, ?)`,
		serviceID, instanceID, revision, at.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("add a member: %w", err)
	}
	return nil
}

func (r *repository) removeMember(ctx context.Context, serviceID, instanceID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM service_members WHERE service_id = ? AND instance_id = ?`,
		serviceID, instanceID)
	if err != nil {
		return fmt.Errorf("remove a member: %w", err)
	}
	return nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM service_members WHERE service_id = ?`, id); err != nil {
		return fmt.Errorf("delete the members: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return tx.Commit()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Service, error) {
	var (
		s                Service
		command, keys    string
		selector         string
		extra, envNames  string
		filePaths        string
		created, updated string
	)
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Replicas, &s.Revision, &s.Template.Isolation,
		&s.Template.Image, &s.Template.ISO, &s.Template.Kernel, &s.Template.DiskGiB,
		&s.Template.FirewallID, &command, &s.Template.NetworkID, &s.Template.RestartPolicy,
		&s.Template.VCPU, &s.Template.MemoryMiB, &s.Template.Group, &s.Template.Strict,
		&selector, &extra, &s.Template.EnvSealed, &s.Template.EnvKeyID, &envNames,
		&s.Template.FilesSealed, &filePaths,
		&keys, &s.Reaped, &s.Blocked, &created, &updated); err != nil {
		return Service{}, fmt.Errorf("scan service: %w", err)
	}

	if err := json.Unmarshal([]byte(command), &s.Template.Command); err != nil {
		return Service{}, fmt.Errorf("decode the command: %w", err)
	}
	if err := json.Unmarshal([]byte(keys), &s.Template.Keys); err != nil {
		return Service{}, fmt.Errorf("decode the keys: %w", err)
	}
	if selector != "" {
		if err := json.Unmarshal([]byte(selector), &s.Template.NodeSelector); err != nil {
			return Service{}, fmt.Errorf("decode the node selector: %w", err)
		}
	}
	if extra != "" {
		if err := json.Unmarshal([]byte(extra), &s.Template.ExtraNetworks); err != nil {
			return Service{}, fmt.Errorf("decode the extra networks: %w", err)
		}
	}
	if envNames != "" {
		if err := json.Unmarshal([]byte(envNames), &s.Template.EnvNames); err != nil {
			return Service{}, fmt.Errorf("decode the environment names: %w", err)
		}
	}
	if filePaths != "" {
		if err := json.Unmarshal([]byte(filePaths), &s.Template.FilePaths); err != nil {
			return Service{}, fmt.Errorf("decode the file paths: %w", err)
		}
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Service{}, fmt.Errorf("parse created_at: %w", err)
	}
	s.CreatedAt = at

	touched, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Service{}, fmt.Errorf("parse updated_at: %w", err)
	}
	s.UpdatedAt = touched
	return s, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
